"""算法工程师需要实现的抽象基类定义（Algorithm SPI）。

本模块提供 **两种清晰分层的接入抽象**，帮助算法团队与 Runtime 基础服务彻底解耦：

1. :class:`FrameAlgorithm`（**帧级推理模式 — 推荐 95% 的算法使用**）
   - Runtime 基础服务自动托管：RTSP 拉流解码、断流指数退避重连、Token 续期无缝切流、
     按 ``target_fps`` 抽帧控频、无目标空帧心跳补发、异常帧自动 JPEG 抓拍编码、租约看门狗回收。
   - 算法工程师只需实现 2 个方法：
     - :meth:`FrameAlgorithm.load`：进程启动时加载模型权重（一次）；
     - :meth:`FrameAlgorithm.infer_frame`：输入单帧 :class:`Frame` 与 :class:`TaskContext`，输出检测框列表。

2. :class:`BaseAlgorithm`（**任务级 / 自定义拉流模式 — 面向特殊高性能管线**）
   - 适用于算法内部自带硬件解码/拉流管线（如 NVIDIA DeepStream、GStreamer、TensorRT C++ 服务代理）
     或纯离线模拟（如 ``MockAlgorithm``）。
   - 算法工程师实现 :meth:`BaseAlgorithm.load` 与 :meth:`BaseAlgorithm.infer_task`，
     仍自动享受 Runtime 提供的 gRPC 服务、多路任务线程调度、配置热更新、租约看门狗与空帧心跳保障。
"""

from __future__ import annotations

from abc import ABC, abstractmethod

from aegis_runtime_sdk.contract.models import (
    AlgorithmMetadata,
    Detection,
    Frame,
    FrameOutput,
    TaskContext,
)


class BaseAlgorithm(ABC):
    """任务级算法抽象基类（自定义拉流 / 底层推理管线接入契约）。

    生命周期调用顺序（均由 Runtime 基础服务严格调度）：
    1. 进程启动时调用一次 :meth:`load` 加载全局模型；成功返回后 gRPC ``Health()`` 才置为 ``ready=True``。
    2. 平台下发 ``AssignTask`` 时调用 :meth:`on_task_start`，随后在独立工作线程中循环调用 :meth:`infer_task`。
    3. 平台下发 ``UpdateTask``（修改 ROI/参数）或 ``RenewLease`` 携带新 ``refreshed_stream_url`` 时，
       调用 :meth:`on_task_update`。
    4. 平台下发 ``RevokeTask``、租约看门狗超时、或进程退出时，Runtime 会先置位 ``task_ctx.stop_event``，
       等待工作线程退出后再调用 :meth:`on_task_stop` 释放单路资源。
    5. 进程优雅停机且所有任务停止后，调用一次 :meth:`unload` 释放全局模型显存。

    线程安全约定：
    - 一个 Runtime 进程可同时运行多路摄像头任务，因此 :meth:`infer_task` 会在多个任务线程中 **并发调用**。
    - 共享模型对象（如 PyTorch / ONNX Runtime Session）若非线程安全，请在子类内部使用推理锁或按线程隔离状态；
      每路任务的跟踪器/历史缓存建议按 ``task_ctx.task_id`` 字典隔离，并在 :meth:`on_task_stop` 中及时清理。
    """

    def metadata(self) -> AlgorithmMetadata:
        """可选：声明算法元信息（名称、版本号、支持的标签列表等）。

        Runtime 会在启动日志与默认版本号回退时读取该信息。
        """
        return AlgorithmMetadata()

    @abstractmethod
    def load(self) -> None:
        """【必须实现】进程启动阶段调用一次，用于加载模型权重并初始化推理引擎（PyTorch / ONNX / TensorRT）。

        注意：
        - 本方法完成前，gRPC ``Health()`` 接口不会就绪；若抛出异常，Runtime 启动会立即失败并上报日志。
        - 请在此完成 GPU 预热（warm-up），避免首帧推理超时。
        """

    def unload(self) -> None:
        """【可选钩子】Runtime 进程优雅退出且所有任务全部停止后调用一次，用于释放全局模型与 GPU 显存。"""

    def health_check(self) -> bool:
        """【可选钩子】供 gRPC ``Health()`` 探针调用的算法健康自检。

        Returns:
            返回 ``True`` 表示算法引擎健康可接收任务；返回 ``False`` 会向 ``aegis-agent`` 报告未就绪。
        """
        return True

    def on_task_start(self, task_ctx: TaskContext) -> None:
        """【可选钩子】新任务分配成功、工作线程启动前调用一次。

        典型用途：为 ``task_ctx.task_id`` 初始化独立的目标跟踪器（Tracker）、打开自定义解码器或状态缓存。
        若本方法抛出异常，Runtime 会拒绝本次 ``AssignTask`` 并将异常信息返回给调度器。

        Args:
            task_ctx: 本次分配的任务上下文。
        """

    def on_task_update(self, task_ctx: TaskContext) -> None:
        """【可选钩子】任务配置热更新时调用。

        触发时机：
        1. 平台修改了该摄像头的 ROI 或 ``algo_params``（``UpdateTask``）；
        2. 平台续租时下发了新的短期鉴权拉流地址 ``refreshed_stream_url``（``RenewLease``）。

        Args:
            task_ctx: 已更新为最新 ROI、参数与 ``stream_url`` 的任务上下文。
        """

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        """【可选钩子】任务停止后调用（撤销、租约过期自动停止、或进程停机）。

        调用前 Runtime 已确保该任务的推理循环线程退出，请在此安全释放单路解码器、跟踪器与显存缓存。

        Args:
            task_ctx: 已停止的任务上下文。
        """

    @abstractmethod
    def infer_task(self, task_ctx: TaskContext) -> FrameOutput | None:
        """【必须实现】执行单路摄像头的一次推理步骤并返回检测结果。

        Runtime 会在该任务对应的工作线程中按 ``task_ctx.target_fps``（或默认间隔）循环调用本方法。

        Args:
            task_ctx: 当前任务运行上下文（含 ``stream_url``、``rois``、``algo_params`` 等）。

        Returns:
            - :class:`FrameOutput`：包含本帧检测目标列表、分辨率以及可选的 ``snapshot_jpeg``；
              当 ``detections`` 为空（有效空画面）时，Runtime 将自动按心跳周期补发空帧心跳
              （``is_heartbeat=True``），以推进平台 ``absence_detect`` 离岗计时。
            - ``None``：表示当前尚未获取到有效画面（如正在断流重连），Runtime **不会**发送空帧心跳，
              从而使平台侧离岗计时自动暂停，避免断流误报。

        注意：
        - 单次推理若偶发异常，Runtime 会捕获并记录日志，不会导致整个进程或其他摄像头任务崩溃。
        """


class FrameAlgorithm(BaseAlgorithm, ABC):
    """帧级推理算法抽象基类（**推荐大多数算法工程师继承本类**）。

    与 :class:`BaseAlgorithm` 的核心区别：
    - **Runtime 基础服务接管拉流**：Runtime 内置的 :class:`FrameSource` 会自动连接 ``task_ctx.stream_url``
      （RTSP over TCP）、处理断流重连与 ``RenewLease`` 刷新后的新 URL 切换，并将解码好的 :class:`Frame`
      直接送入 :meth:`infer_frame`。
    - **自动异常抓拍**：当算法返回异常目标（任意 ``Detection.is_abnormal == True``）且未手动提供
      ``snapshot_jpeg`` 时，Runtime 会自动调用 :meth:`Frame.encode_jpeg` 生成抓拍图。
    - **算法只需聚焦模型本身**：只需实现 :meth:`load` 和 :meth:`infer_frame` 两个方法即可上线。

    Example::

        class HelmetAlgorithm(FrameAlgorithm):
            def load(self) -> None:
                self.model = load_yolo("helmet.onnx")

            def infer_frame(self, frame: Frame, task_ctx: TaskContext) -> list[Detection]:
                preds = self.model(frame.image, conf=task_ctx.confidence_threshold)
                return [
                    Detection.from_xyxy_pixels(
                        x1, y1, x2, y2, frame.width, frame.height,
                        label=label,
                        confidence=score,
                        is_abnormal=(label == "no_helmet"),
                    )
                    for x1, y1, x2, y2, label, score in preds
                ]
    """

    #: 当检测到 ``is_abnormal=True`` 的目标且算法未自行编码 ``snapshot_jpeg`` 时，
    #: 是否由 Runtime 自动从当前 :class:`Frame` 编码 JPEG 抓拍图（默认 ``True``）。
    auto_snapshot_on_abnormal: bool = True

    #: 若为 ``True``，只要 ``detections`` 非空即自动附带抓拍图（即使 ``is_abnormal=False``）；
    #: 适用于需要由平台规则引擎触发报警并直接复用 Runtime 抓拍图的普通标签检测（默认 ``False``，按需开启）。
    auto_snapshot_on_any_detection: bool = False

    #: 自动抓拍时的 JPEG 压缩质量（1..100，默认 85）。
    snapshot_jpeg_quality: int = 85

    @abstractmethod
    def infer_frame(
        self,
        frame: Frame,
        task_ctx: TaskContext,
    ) -> list[Detection] | FrameOutput | None:
        """【必须实现】对单帧解码图像执行模型推理。

        Args:
            frame: 由 Runtime 拉流服务解码得到的单帧图像（含 ``image``、``width``、``height``、``timestamp_ms``）。
            task_ctx: 当前任务运行上下文（含 ``rois``、``algo_params``、``confidence_threshold`` 等）。

        Returns:
            支持以下三种便捷返回形式：
            1. ``list[Detection]``：最简洁形式，Runtime 自动填充 ``frame_width``/``frame_height``
               并按策略自动生成 ``snapshot_jpeg``；
            2. :class:`FrameOutput`：完全自定义输出（例如需要自行在抓拍图上做特殊处理或跳过自动编码）；
            3. ``None``：跳过本帧输出（例如画面质量过差需丢弃）。
        """

    def infer_task(self, task_ctx: TaskContext) -> FrameOutput | None:
        """任务级入口的占位实现。

        在 :class:`FrameAlgorithm` 模式下，Runtime 的 ``TaskManager`` 会自动驱动拉流并直接调用
        :meth:`infer_frame`，算法工程师 **无需** 重写本方法。
        """
        raise NotImplementedError(
            "FrameAlgorithm is driven by Runtime TaskManager via infer_frame(frame, task_ctx); "
            "do not call infer_task() directly."
        )
