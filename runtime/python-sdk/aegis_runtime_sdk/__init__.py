"""AegisVision Python Algorithm Runtime SDK.

本 SDK 在结构上严格划分为两个职责边界清晰的层级：

1. **算法接入契约层（``aegis_runtime_sdk.contract``）— 给算法工程师阅读与实现**：
   - 抽象基类：:class:`FrameAlgorithm`（推荐，纯帧级推理）、:class:`BaseAlgorithm`（自定义拉流管线）；
   - 数据结构体：:class:`Point`、:class:`ROI`、:class:`Detection`、:class:`Frame`、
     :class:`FrameOutput`、:class:`TaskContext`、:class:`AlgorithmMetadata`。

2. **Runtime 基础服务层（``aegis_runtime_sdk.runtime``）— 平台托管基础设施**：
   - 服务入口与配置：:func:`serve`、:class:`RuntimeServer`、:class:`RuntimeConfig`；
   - 底层组件：:class:`TaskManager`、:class:`LeaseWatchdog`、:class:`ResultBus`、
     :class:`FrameSource`、:class:`OpenCVStreamSource`。
"""

# 1. 算法接入契约层（给算法工程师看 / 实现）
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

# 2. Runtime 基础服务层（平台托管，算法入口直接调用 serve() 即可）
from aegis_runtime_sdk.runtime import (
    FrameSource,
    FrameSourceFactory,
    LeaseWatchdog,
    OpenCVStreamSource,
    ResultBus,
    RuntimeConfig,
    RuntimeServer,
    TaskManager,
    serve,
)

__all__ = [
    # Algorithm Contract Layer
    "AlgorithmMetadata",
    "BaseAlgorithm",
    "Detection",
    "Frame",
    "FrameAlgorithm",
    "FrameOutput",
    "Point",
    "ROI",
    "TaskContext",
    # Runtime Foundation Services Layer
    "FrameSource",
    "FrameSourceFactory",
    "LeaseWatchdog",
    "OpenCVStreamSource",
    "ResultBus",
    "RuntimeConfig",
    "RuntimeServer",
    "TaskManager",
    "serve",
]
