"""向后兼容导出模块：请优先从 ``aegis_runtime_sdk.contract`` 或顶层 ``aegis_runtime_sdk`` 导入。"""

from aegis_runtime_sdk.contract import (
    AlgorithmMetadata,
    BaseAlgorithm,
    Detection,
    Frame,
    FrameAlgorithm,
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
