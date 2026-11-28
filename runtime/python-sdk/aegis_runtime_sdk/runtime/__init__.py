"""Runtime 基础服务层（Runtime Foundation Services）。

本包封装了 AegisVision 平台托管的全部底层基础设施，**算法工程师通常无需修改或重新实现这些模块**，
只需在入口处调用 :func:`serve` 即可自动获得以下能力：

- :class:`RuntimeConfig`：统一配置与 ``AEGIS_RUNTIME_*`` 环境变量解析；
- :class:`LeaseWatchdog`：租约看门狗，防止 ``aegis-agent`` 异常断开后任务残留占用显存；
- :class:`FrameSource` / :class:`OpenCVStreamSource`：低延迟 RTSP 拉流、断流退避重连与 Token 刷新切流；
- :class:`ResultBus`：有界队列多订阅者分发、慢客户端丢旧保新、坐标裁剪与 Protobuf 封装；
- :class:`TaskManager`：多路任务并发调度、动态 ``target_fps`` 控频、空帧心跳与异常自动抓拍；
- :class:`RuntimeServer` / :func:`serve`：gRPC ``AlgorithmRuntime`` 协议适配与优雅停机。
"""

from aegis_runtime_sdk.runtime.config import RuntimeConfig
from aegis_runtime_sdk.runtime.lease import LeaseWatchdog
from aegis_runtime_sdk.runtime.result_bus import ResultBus
from aegis_runtime_sdk.runtime.server import RuntimeServer, serve
from aegis_runtime_sdk.runtime.stream import FrameSource, FrameSourceFactory, OpenCVStreamSource
from aegis_runtime_sdk.runtime.task_manager import TaskManager

__all__ = [
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
