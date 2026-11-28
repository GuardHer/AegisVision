"""算法接入契约层（Algorithm Contract Layer）。

本包定义了 **算法工程师需要阅读、使用或实现的全部数据结构与抽象基类**，
与 Runtime 底层基础服务（gRPC 通信、租约看门狗、RTSP 拉流、任务线程调度、心跳与队列分发）彻底解耦。

快速选型指引：
- 绝大多数 PyTorch / YOLO / ONNX / TensorRT 图像检测算法：
  继承 :class:`FrameAlgorithm`，只需实现 ``load()`` 与 ``infer_frame(frame, task_ctx)``；
  RTSP 拉流、断流重连、Token 续期切流、控频、空帧心跳、异常帧 JPEG 抓拍全部由 Runtime 自动托管。
- 自带视频解码管线的算法（如 DeepStream / GStreamer / 外部推理服务代理 / Mock 测试）：
  继承 :class:`BaseAlgorithm`，实现 ``load()`` 与 ``infer_task(task_ctx)``。
"""

from aegis_runtime_sdk.contract.algorithm import BaseAlgorithm, FrameAlgorithm
from aegis_runtime_sdk.contract.models import (
    AlgorithmMetadata,
    Detection,
    Frame,
    FrameOutput,
    Point,
    ROI,
    TaskContext,
)

__all__ = [
    "AlgorithmMetadata",
    "BaseAlgorithm",
    "Detection",
    "Frame",
    "FrameAlgorithm",
    "FrameOutput",
    "Point",
    "ROI",
    "TaskContext",
]
