"""算法接入契约数据结构（给算法工程师阅读与使用的全部结构体）。

坐标系与字段规范（与 AegisVision 平台前端 Canvas、规则引擎严格一致）：
1. **归一化坐标**：所有顶点 (:class:`Point`) 与检测框 (:attr:`Detection.bbox`) 均使用
   ``[0.0, 1.0]`` 归一化坐标，原点 ``(0.0, 0.0)`` 位于画面左上角，``(1.0, 1.0)`` 位于画面右下角。
   - 若模型输出为像素坐标，可直接调用 :meth:`Detection.from_xyxy_pixels` 或
     :meth:`Detection.from_xywh_pixels` 自动换算并裁剪，避免手写归一化出错。
2. **标签约定**：:attr:`Detection.label` 必须与平台「算法仓库」中声明的 ``supported_labels``
   以及规则配置中的 ``labels`` 保持一致（如 ``"person"``、``"no_helmet"``、``"wear_esd_strap"``）。
3. **规则引擎分工**：ROI 命中判定、连续帧防抖、持续时长/离岗计时、冷却抑制与 SOP 顺序编排
   均由平台侧 ``aegis-ingest`` 规则引擎统一完成；算法侧只需专注于输出准确、稳定的目标检测框与追踪 ID。
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Any, Callable


def _clamp01(value: float) -> float:
    """将浮点数安全裁剪至 [0.0, 1.0] 归一化区间。"""
    v = float(value)
    if v < 0.0:
        return 0.0
    if v > 1.0:
        return 1.0
    return v


@dataclass(frozen=True)
class Point:
    """归一化二维坐标点（不可变结构体）。

    Attributes:
        x: 横坐标归一化值，范围 ``[0.0, 1.0]``，``0.0`` 为画面最左侧，``1.0`` 为画面最右侧。
        y: 纵坐标归一化值，范围 ``[0.0, 1.0]``，``0.0`` 为画面最顶部，``1.0`` 为画面最底部。
    """

    x: float
    y: float

    def to_pixel(self, frame_width: int, frame_height: int) -> tuple[int, int]:
        """将当前归一化坐标转换为指定分辨率下的整数像素坐标 ``(px, py)``。

        Args:
            frame_width: 图像宽度（像素，必须 > 0）。
            frame_height: 图像高度（像素，必须 > 0）。

        Returns:
            ``(px, py)`` 像素坐标元组，范围分别为 ``[0, frame_width]`` 与 ``[0, frame_height]``。
        """
        px = int(round(_clamp01(self.x) * max(0, frame_width)))
        py = int(round(_clamp01(self.y) * max(0, frame_height)))
        return (px, py)


@dataclass(frozen=True)
class ROI:
    """平台下发的感兴趣检测区域（Region of Interest，不可变结构体）。

    算法工程师既可以将 ROI 仅作为参考（全图推理后把全部目标交由平台规则引擎做 ROI 判定），
    也可以在推理前利用 :meth:`to_pixel_points` 裁剪/掩码加速推理，或用 :meth:`contains_point` 做预过滤。

    Attributes:
        roi_id: ROI 唯一标识（UUID 字符串），由平台生成。
        name: 区域名称（如 ``"1号工位操作区"``、``"全画面"``）。
        roi_type: 区域类型，取值为：
            - ``"polygon"``：多边形区域，顶点列表见 :attr:`points`（至少 3 个点，按顺/逆时针排列）；
            - ``"full_frame"``：全画面区域，此时 :attr:`points` 通常为空列表。
        points: 归一化多边形顶点列表（每个元素为 :class:`Point`）。
    """

    roi_id: str
    name: str
    roi_type: str
    points: list[Point] = field(default_factory=list)

    def is_full_frame(self) -> bool:
        """判断当前 ROI 是否为全画面区域。

        Returns:
            当 ``roi_type == "full_frame"`` 或多边形顶点不足 3 个时返回 ``True``。
        """
        return self.roi_type == "full_frame" or len(self.points) < 3

    def to_pixel_points(self, frame_width: int, frame_height: int) -> list[tuple[int, int]]:
        """将 ROI 顶点换算为像素坐标列表，便于直接传给 OpenCV（如 ``cv2.fillPoly`` / ``cv2.polylines``）。

        Args:
            frame_width: 当前视频帧宽度（像素）。
            frame_height: 当前视频帧高度（像素）。

        Returns:
            像素坐标元组列表 ``[(x1, y1), (x2, y2), ...]``。
            若为全画面 ROI，返回覆盖整个画面的矩形四角坐标。
        """
        if self.is_full_frame():
            w = max(0, frame_width)
            h = max(0, frame_height)
            return [(0, 0), (w, 0), (w, h), (0, h)]
        return [p.to_pixel(frame_width, frame_height) for p in self.points]

    def contains_point(self, x: float, y: float) -> bool:
        """使用射线法判断给定归一化坐标 ``(x, y)`` 是否位于当前 ROI 内。

        Args:
            x: 待测点归一化横坐标，范围 ``[0.0, 1.0]``。
            y: 待测点归一化纵坐标，范围 ``[0.0, 1.0]``。

        Returns:
            点在区域内（或当前为全画面 ROI）返回 ``True``，否则返回 ``False``。
        """
        if self.is_full_frame():
            return 0.0 <= x <= 1.0 and 0.0 <= y <= 1.0

        inside = False
        pts = self.points
        n = len(pts)
        j = n - 1
        for i in range(n):
            xi, yi = pts[i].x, pts[i].y
            xj, yj = pts[j].x, pts[j].y
            intersects = ((yi > y) != (yj > y)) and (
                x < (xj - xi) * (y - yi) / ((yj - yi) or 1e-12) + xi
            )
            if intersects:
                inside = not inside
            j = i
        return inside


@dataclass
class Detection:
    """单帧图像中的单个目标检测结果。

    这是算法侧最核心的输出结构体，每个检测到的目标对应一个 ``Detection`` 实例。

    Attributes:
        bbox: 目标边界框归一化坐标 ``(x1, y1, x2, y2)``，均在 ``[0.0, 1.0]`` 内，
            满足 ``x1 <= x2`` 且 ``y1 <= y2``（左上角到右下角）。
        label: 目标类别标签字符串（如 ``"person"``、``"no_helmet"``、``"smoking"``）。
            必须与平台算法仓库中的 ``supported_labels`` 对齐。
        confidence: 模型预测置信度，范围 ``[0.0, 1.0]``。
        track_id: 可选的多目标跟踪 ID（如 ByteTrack / BoT-SORT 分配的 ``"trk-12"``）。
            若算法开启追踪请填入，便于前端显示轨迹编号以及 SOP/滞留规则按人员区分；无追踪可留空 ``""``。
        is_abnormal: 是否由算法判定为视觉异常目标（默认 ``False``）：
            - 当为 ``True`` 时，前端画框会以高危红色高亮，且在 :class:`FrameAlgorithm` 模式下
              Runtime 会自动触发抓拍图编码（若算法未手动提供 ``snapshot_jpeg``）；
            - 普通计数、离岗或 SOP 动作识别目标通常保持 ``False``，最终是否报警由平台规则引擎裁决。
        attributes: 可选的扩展键值属性字典（键和值均为 ``str``），例如
            ``{"helmet_color": "yellow", "posture": "standing"}``，将随告警快照落库展示。
    """

    bbox: tuple[float, float, float, float]
    label: str
    confidence: float
    track_id: str = ""
    is_abnormal: bool = False
    attributes: dict[str, str] = field(default_factory=dict)

    @classmethod
    def from_xyxy_pixels(
        cls,
        x1: float,
        y1: float,
        x2: float,
        y2: float,
        frame_width: int,
        frame_height: int,
        *,
        label: str,
        confidence: float,
        track_id: str = "",
        is_abnormal: bool = False,
        attributes: dict[str, str] | None = None,
    ) -> Detection:
        """从像素坐标 ``(x1, y1, x2, y2)`` 构造 :class:`Detection`，自动完成归一化与越界裁剪。

        推荐 YOLO / ONNX / TensorRT 算法直接使用此工厂方法，避免手算归一化坐标或出现越界值。

        Args:
            x1: 左上角像素 X 坐标。
            y1: 左上角像素 Y 坐标。
            x2: 右下角像素 X 坐标。
            y2: 右下角像素 Y 坐标。
            frame_width: 当前帧宽度（像素，必须 > 0）。
            frame_height: 当前帧高度（像素，必须 > 0）。
            label: 目标类别标签。
            confidence: 预测置信度，范围 ``[0.0, 1.0]``。
            track_id: 可选目标跟踪 ID。
            is_abnormal: 是否标记为异常目标。
            attributes: 可选扩展属性字典。

        Returns:
            已完成归一化并裁剪至 ``[0.0, 1.0]`` 的 :class:`Detection` 实例。

        Raises:
            ValueError: 当 ``frame_width <= 0`` 或 ``frame_height <= 0`` 时抛出。
        """
        if frame_width <= 0 or frame_height <= 0:
            raise ValueError("frame_width and frame_height must be positive integers")
        nx1 = _clamp01(min(x1, x2) / frame_width)
        ny1 = _clamp01(min(y1, y2) / frame_height)
        nx2 = _clamp01(max(x1, x2) / frame_width)
        ny2 = _clamp01(max(y1, y2) / frame_height)
        return cls(
            bbox=(nx1, ny1, nx2, ny2),
            label=label,
            confidence=_clamp01(confidence),
            track_id=track_id,
            is_abnormal=is_abnormal,
            attributes=dict(attributes) if attributes else {},
        )

    @classmethod
    def from_xywh_pixels(
        cls,
        x: float,
        y: float,
        w: float,
        h: float,
        frame_width: int,
        frame_height: int,
        *,
        label: str,
        confidence: float,
        track_id: str = "",
        is_abnormal: bool = False,
        attributes: dict[str, str] | None = None,
    ) -> Detection:
        """从像素坐标 ``(x, y, w, h)`` 构造 :class:`Detection`（左上角坐标 + 宽高）。

        Args:
            x: 左上角像素 X 坐标。
            y: 左上角像素 Y 坐标。
            w: 目标框像素宽度。
            h: 目标框像素高度。
            frame_width: 当前帧宽度（像素，必须 > 0）。
            frame_height: 当前帧高度（像素，必须 > 0）。
            label: 目标类别标签。
            confidence: 预测置信度，范围 ``[0.0, 1.0]``。
            track_id: 可选目标跟踪 ID。
            is_abnormal: 是否标记为异常目标。
            attributes: 可选扩展属性字典。

        Returns:
            归一化后的 :class:`Detection` 实例。
        """
        return cls.from_xyxy_pixels(
            x,
            y,
            x + max(0.0, w),
            y + max(0.0, h),
            frame_width,
            frame_height,
            label=label,
            confidence=confidence,
            track_id=track_id,
            is_abnormal=is_abnormal,
            attributes=attributes,
        )

    def clamped_bbox(self) -> tuple[float, float, float, float]:
        """返回经 ``[0.0, 1.0]`` 防御裁剪且保证 ``x1 <= x2, y1 <= y2`` 的边界框元组。"""
        if len(self.bbox) != 4:
            return (0.0, 0.0, 0.0, 0.0)
        x1, y1, x2, y2 = self.bbox
        nx1, nx2 = _clamp01(min(x1, x2)), _clamp01(max(x1, x2))
        ny1, ny2 = _clamp01(min(y1, y2)), _clamp01(max(y1, y2))
        return (nx1, ny1, nx2, ny2)

    def center(self) -> Point:
        """返回检测框的归一化中心点 :class:`Point`。"""
        x1, y1, x2, y2 = self.clamped_bbox()
        return Point(x=(x1 + x2) / 2.0, y=(y1 + y2) / 2.0)

    def bottom_center(self) -> Point:
        """返回检测框的归一化底边中点（常用于人员脚部位置 ROI 落点判断）。"""
        x1, y1, x2, y2 = self.clamped_bbox()
        return Point(x=(x1 + x2) / 2.0, y=y2)


@dataclass
class Frame:
    """由 Runtime 拉流解码基础服务提供给帧级算法（:class:`FrameAlgorithm`）的单帧图像容器。

    算法工程师在 ``infer_frame(frame, task_ctx)`` 中直接消费本对象，
    无需关心底层 RTSP 连接、重连、Token 续期或抽帧逻辑。

    Attributes:
        image: 解码后的图像数据（使用默认 ``OpenCVStreamSource`` 时为 ``numpy.ndarray``，
            形状 ``(H, W, 3)``、通道顺序 BGR；在单元测试或自定义 ``FrameSource`` 中可为任意图像对象）。
        width: 图像宽度（像素）。
        height: 图像高度（像素）。
        timestamp_ms: 采集到本帧时的 Unix 毫秒时间戳。
        frame_index: 当前任务启动以来的单调递增帧序号（从 1 开始）。
        color_format: 图像色彩格式说明，默认为 ``"bgr"``（OpenCV 标准格式），也可为 ``"rgb"``。
        _jpeg_encoder: Runtime 内部注入的可选编码器回调，算法侧无需直接访问。
    """

    image: Any
    width: int
    height: int
    timestamp_ms: int
    frame_index: int = 0
    color_format: str = "bgr"
    _jpeg_encoder: Callable[[Any, int], bytes] | None = field(default=None, repr=False)

    def encode_jpeg(self, quality: int = 85) -> bytes:
        """将当前帧编码为 JPEG 字节流，用于填充 :attr:`FrameOutput.snapshot_jpeg` 作为告警证据抓拍图。

        Args:
            quality: JPEG 压缩质量，范围 ``1..100``，默认 ``85``（兼顾清晰度与网络体积）。

        Returns:
            编码成功时返回 JPEG 二进制 ``bytes``；若当前环境未安装 ``cv2`` 或编码失败则返回空字节 ``b""``。
        """
        q = max(1, min(100, int(quality)))
        if self._jpeg_encoder is not None:
            try:
                return self._jpeg_encoder(self.image, q)
            except Exception:
                return b""
        if self.image is None:
            return b""
        try:
            import cv2  # type: ignore[import-not-found]

            ok, buf = cv2.imencode(".jpg", self.image, [int(cv2.IMWRITE_JPEG_QUALITY), q])
            return bytes(buf.tobytes()) if ok else b""
        except Exception:
            return b""


@dataclass
class FrameOutput:
    """算法单次推理返回给 Runtime 的结果载体。

    Runtime 基础服务会负责将本对象转换为 Protobuf ``RuntimeResult`` 并通过 gRPC 推送给 ``aegis-agent``：
    - 当 ``detections`` 非空时，立即作为有效检测帧发送（``is_heartbeat=False``）；
    - 当 ``detections`` 为空时，Runtime 会按心跳周期自动补发 ``is_heartbeat=True`` 的空帧心跳，
      保证平台侧 ``absence_detect``（离岗检测）精准计时且不产生误报。

    Attributes:
        detections: 本帧检测到的全部目标列表（:class:`Detection`）。无目标时返回空列表 ``[]``。
        frame_width: 本帧图像原始宽度（像素），用于前端叠加层按 ``object-fit: contain`` 精确换算。
        frame_height: 本帧图像原始高度（像素）。
        snapshot_jpeg: 可选的告警抓拍图 JPEG 字节流。
            - 在 :class:`FrameAlgorithm` 模式下：若检测到异常目标（任意 ``d.is_abnormal == True``）
              且本字段为空，Runtime 会自动调用 :meth:`Frame.encode_jpeg` 补齐，算法侧通常无需手动编码；
            - 在 :class:`BaseAlgorithm` 模式下：建议仅在异常帧按需编码填入，避免每帧无意义编码拖慢吞吐。
    """

    detections: list[Detection] = field(default_factory=list)
    frame_width: int = 0
    frame_height: int = 0
    snapshot_jpeg: bytes = b""

    @property
    def has_detections(self) -> bool:
        """本帧是否包含至少一个检测目标。"""
        return len(self.detections) > 0

    @property
    def has_abnormal(self) -> bool:
        """本帧是否包含至少一个被标记为 ``is_abnormal=True`` 的目标。"""
        return any(d.is_abnormal for d in self.detections)


@dataclass
class TaskContext:
    """平台下发给单路摄像头推理任务的运行上下文。

    每个活跃任务拥有独立的 ``TaskContext`` 实例；当平台通过 ``UpdateTask`` 修改 ROI/参数，
    或通过 ``RenewLease`` 刷新流地址 Token 时，Runtime 会原地更新对应字段并触发算法的
    ``on_task_update(task_ctx)`` 钩子。

    Attributes:
        task_id: 推理任务唯一标识（UUID），同一 ``(camera_id, algorithm_version_id)`` 对应一个任务。
        camera_id: 摄像头点位唯一标识（UUID）。
        algorithm_version_id: 算法版本唯一标识（UUID）。
        stream_url: 当前任务的视频拉流地址（平台默认下发授权内部低延迟 RTSP 地址，带短期 Token）。
            注意：Token 续期时 Runtime 会通过 ``RenewLease`` 自动刷新本字段。
        stream_protocol: 拉流协议标识，默认为 ``"rtsp"``。
        rois: 当前摄像头上绑定本算法版本的全部启用 ROI 区域列表（:class:`ROI`）。
        algo_params: 算法超参数字典（由平台算法版本默认参数与规则级 ``algo_params_override`` 合并生成），
            例如 ``{"target_fps": 5, "min_confidence": 0.6, "iou_threshold": 0.45}``。
        lease_id: 当前任务租约 ID（由平台调度器签发，用于 Runtime 看门狗续租校验）。
        lease_ttl_seconds: 租约存活时长（秒），超过该时长未续租由 Runtime 自动停止任务释放显存。
        config_version: 配置版本号（单调递增，热更新 ROI 或参数时递增）。
        stop_event: 任务停止信号（:class:`threading.Event`）。任务被撤销、租约过期或进程关闭时置位。
            算法若有内部循环，必须定期检查 :meth:`is_stopped` 或使用 :meth:`sleep` 等待。
    """

    task_id: str
    camera_id: str
    algorithm_version_id: str
    stream_url: str
    stream_protocol: str
    rois: list[ROI]
    algo_params: dict[str, Any]
    lease_id: str
    lease_ttl_seconds: int
    config_version: int
    stop_event: threading.Event = field(default_factory=threading.Event)

    def is_stopped(self) -> bool:
        """检查当前任务是否已被要求停止（撤销、租约过期或进程退出）。"""
        return self.stop_event.is_set()

    def sleep(self, seconds: float) -> bool:
        """可中断休眠：等待 ``seconds`` 秒，若期间任务收到停止信号则立即唤醒。

        Args:
            seconds: 等待时长（秒）。

        Returns:
            若因任务停止而被提前唤醒返回 ``True``，正常等待超时返回 ``False``。
        """
        return self.stop_event.wait(max(0.0, float(seconds)))

    def get_param(self, key: str, default: Any = None) -> Any:
        """安全读取 :attr:`algo_params` 中的指定超参数。

        Args:
            key: 参数名（如 ``"min_confidence"``、``"target_fps"``）。
            default: 键不存在或值为 ``None`` 时返回的默认值。

        Returns:
            对应的参数值或 ``default``。
        """
        value = self.algo_params.get(key, default)
        return default if value is None else value

    @property
    def target_fps(self) -> float | None:
        """读取任务配置的目标推理帧率 ``algo_params["target_fps"]``。

        Runtime 调度器会自动依据该值控制单任务推理频率；若未配置或值 <= 0，则返回 ``None``
        （使用 Runtime 默认推理间隔）。
        """
        raw = self.algo_params.get("target_fps")
        if raw is None:
            return None
        try:
            fps = float(raw)
            return fps if fps > 0 else None
        except (TypeError, ValueError):
            return None

    @property
    def confidence_threshold(self) -> float:
        """便捷读取置信度阈值（依次查找 ``min_confidence`` / ``confidence``，默认 ``0.5``）。"""
        raw = self.algo_params.get("min_confidence", self.algo_params.get("confidence", 0.5))
        try:
            return _clamp01(float(raw))
        except (TypeError, ValueError):
            return 0.5


@dataclass(frozen=True)
class AlgorithmMetadata:
    """算法元信息声明（可选返回，用于日志、健康检查与自检）。

    Attributes:
        runtime_key: 推荐的 Runtime 键标识（如 ``"py-yolo-helmet-v1"``），与 ``agent.yaml`` 及平台算法版本对应。
        version: 算法语义化版本号（如 ``"1.2.0"``）。
        supported_labels: 本算法可输出的全部标签元组（如 ``("person", "no_helmet")``）。
        description: 算法功能简述。
    """

    runtime_key: str = ""
    version: str = "0.1.0"
    supported_labels: tuple[str, ...] = ()
    description: str = ""
