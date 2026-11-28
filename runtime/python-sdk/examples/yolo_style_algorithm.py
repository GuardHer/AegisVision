"""算法接入示范模板：基于 FrameAlgorithm 的标准视觉检测算法（YOLO / ONNX 风格）。

本示例展示 **算法工程师最小需要编写的代码**：
- 只需从 ``aegis_runtime_sdk.contract`` 导入契约类，继承 :class:`FrameAlgorithm`；
- 实现 ``load()`` 与 ``infer_frame(frame, task_ctx)``；
- 入口处调用 ``serve(HelmetDetectorAlgorithm())`` 启动服务；
- RTSP 拉流、断流重连、Token 刷新切流、目标帧率控频、无目标空帧心跳、异常帧自动抓拍、
  gRPC 通信与租约看门狗全部由 Runtime 基础服务自动完成。
"""

from __future__ import annotations

import logging
import threading

from aegis_runtime_sdk.contract import (
    AlgorithmMetadata,
    Detection,
    Frame,
    FrameAlgorithm,
    TaskContext,
)
from aegis_runtime_sdk.runtime import serve

logger = logging.getLogger("aegis.example.helmet")


class HelmetDetectorAlgorithm(FrameAlgorithm):
    """安全帽检测示范算法。"""

    # 当返回任意 is_abnormal=True 的目标时，由 Runtime 自动调用 frame.encode_jpeg() 附带抓拍图
    auto_snapshot_on_abnormal = True
    snapshot_jpeg_quality = 85

    def __init__(self, weights_path: str = "models/helmet_v1.onnx") -> None:
        self._weights_path = weights_path
        self._infer_lock = threading.Lock()
        self._session = None
        # 每路任务独立的状态（如跟踪器实例），务必按 task_id 隔离
        self._trackers: dict[str, dict[str, int]] = {}

    def metadata(self) -> AlgorithmMetadata:
        return AlgorithmMetadata(
            runtime_key="py-yolo-helmet-v1",
            version="1.0.0",
            supported_labels=("person", "helmet", "no_helmet"),
            description="Example helmet compliance detector using FrameAlgorithm",
        )

    def load(self) -> None:
        """进程启动时加载一次模型权重（此处用占位对象演示，真实项目替换为 YOLO / ONNX / TensorRT 加载）。"""
        logger.info("Loading model weights from %s ...", self._weights_path)
        self._session = {"weights": self._weights_path, "ready": True}

    def unload(self) -> None:
        """进程退出时释放模型与显存资源。"""
        self._session = None
        self._trackers.clear()
        logger.info("Model resources released")

    def health_check(self) -> bool:
        return self._session is not None

    def on_task_start(self, task_ctx: TaskContext) -> None:
        """为新分配的摄像头任务初始化独立跟踪状态。"""
        self._trackers[task_ctx.task_id] = {"frames": 0}
        logger.info(
            "Task %s started on camera=%s with %d ROIs (target_fps=%s)",
            task_ctx.task_id,
            task_ctx.camera_id,
            len(task_ctx.rois),
            task_ctx.target_fps,
        )

    def on_task_update(self, task_ctx: TaskContext) -> None:
        """平台热更新 ROI 或参数时触发。"""
        logger.info(
            "Task %s updated to config_version=%d (conf=%.2f)",
            task_ctx.task_id,
            task_ctx.config_version,
            task_ctx.confidence_threshold,
        )

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        """任务撤销或租约过期后释放该摄像头专属状态。"""
        self._trackers.pop(task_ctx.task_id, None)
        logger.info("Task %s stopped and cleaned up", task_ctx.task_id)

    def infer_frame(self, frame: Frame, task_ctx: TaskContext) -> list[Detection]:
        """核心单帧推理：输入解码好的 Frame，返回归一化后的 Detection 列表。"""
        state = self._trackers.setdefault(task_ctx.task_id, {"frames": 0})
        state["frames"] += 1
        conf_thresh = task_ctx.confidence_threshold

        # 进阶用法：若单 Runtime 含多个子模型，可按 task_ctx.required_labels / roi.target_labels 按需跳过未配置分支
        required = task_ctx.required_labels
        if required and "no_helmet" not in required and "helmet" not in required and "person" not in required:
            return []

        # 若底层模型推理非线程安全，通过锁保护前向计算（尽量不要在锁内做耗时的图片编码或 IO）
        with self._infer_lock:
            # 此处替换为真实模型前向推理：raw_boxes = self._session.predict(frame.image, conf=conf_thresh)
            raw_boxes = [
                # (x1_px, y1_px, x2_px, y2_px, label, confidence, track_id)
                (320, 180, 560, 720, "no_helmet", 0.91, "trk-1"),
            ]

        detections: list[Detection] = []
        for x1, y1, x2, y2, label, score, track_id in raw_boxes:
            if score < conf_thresh:
                continue
            # 使用工厂方法将像素坐标自动转换为 [0.0, 1.0] 归一化坐标
            det = Detection.from_xyxy_pixels(
                x1,
                y1,
                x2,
                y2,
                frame.width,
                frame.height,
                label=label,
                confidence=score,
                track_id=track_id,
                is_abnormal=(label == "no_helmet"),
                attributes={"frame_idx": str(frame.frame_index)},
            )
            detections.append(det)

        return detections


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    serve(HelmetDetectorAlgorithm())


if __name__ == "__main__":
    main()
