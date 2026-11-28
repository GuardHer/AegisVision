"""Runtime 基础服务统一配置模型（支持 ``AEGIS_RUNTIME_*`` 环境变量注入）。"""

from __future__ import annotations

import os
from dataclasses import dataclass


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name)
    if raw is None or not raw.strip():
        return default
    try:
        return float(raw)
    except ValueError:
        return default


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name)
    if raw is None or not raw.strip():
        return default
    try:
        return int(raw)
    except ValueError:
        return default


@dataclass
class RuntimeConfig:
    """AegisVision Python Runtime 基础服务运行配置。

    Attributes:
        runtime_key: 本 Runtime 进程唯一类型键（需与 ``agent.yaml`` 及平台算法版本中的 ``runtime_key`` 一致），
            默认读取环境变量 ``AEGIS_RUNTIME_KEY``。
        bind_addr: gRPC 监听地址（由 ``aegis-agent`` 注入 ``AEGIS_RUNTIME_ADDR``，默认 ``127.0.0.1:50051``）。
        version: Runtime 版本号（向 ``Health()`` 接口汇报）。
        heartbeat_interval_sec: 无目标场景下的空帧心跳发送间隔（秒），默认 ``1.0`` 秒。
            用于保障平台 ``absence_detect`` 离岗检测计时准确。
        watchdog_interval_sec: 租约看门狗扫描周期（秒），默认 ``0.25`` 秒。
        default_infer_interval_sec: 当任务未在 ``algo_params["target_fps"]`` 指定帧率时的默认推理间隔（秒），
            默认 ``0.2`` 秒（相当于 5 FPS）。
        max_tasks: 单 Runtime 进程允许并发的最大任务数；``0`` 表示不限制（由 ``aegis-agent`` 侧控制）。
        subscriber_queue_size: ``StreamResults`` 单个订阅者的有界结果队列长度，超出时丢旧保新，默认 ``128``。
        grpc_max_workers: gRPC 服务端线程池大小，默认 ``16``。
        shutdown_grace_sec: 收到 ``SIGINT``/``SIGTERM`` 后的 gRPC 优雅停机等待秒数，默认 ``5.0`` 秒。
    """

    runtime_key: str = "py-default"
    bind_addr: str = "127.0.0.1:50051"
    version: str = "0.1.0"
    heartbeat_interval_sec: float = 1.0
    watchdog_interval_sec: float = 0.25
    default_infer_interval_sec: float = 0.2
    max_tasks: int = 0
    subscriber_queue_size: int = 128
    grpc_max_workers: int = 16
    shutdown_grace_sec: float = 5.0

    @classmethod
    def from_env(
        cls,
        *,
        runtime_key: str | None = None,
        bind_addr: str | None = None,
        version: str | None = None,
    ) -> RuntimeConfig:
        """从 ``AEGIS_RUNTIME_*`` 环境变量构造 :class:`RuntimeConfig`，显式参数优先级更高。"""
        return cls(
            runtime_key=runtime_key or os.getenv("AEGIS_RUNTIME_KEY", "py-default"),
            bind_addr=bind_addr or os.getenv("AEGIS_RUNTIME_ADDR", "127.0.0.1:50051"),
            version=version or os.getenv("AEGIS_RUNTIME_VERSION", "0.1.0"),
            heartbeat_interval_sec=max(0.05, _env_float("AEGIS_RUNTIME_HEARTBEAT_SEC", 1.0)),
            watchdog_interval_sec=max(0.02, _env_float("AEGIS_RUNTIME_WATCHDOG_SEC", 0.25)),
            default_infer_interval_sec=max(0.01, _env_float("AEGIS_RUNTIME_INFER_INTERVAL_SEC", 0.2)),
            max_tasks=max(0, _env_int("AEGIS_RUNTIME_MAX_TASKS", 0)),
            subscriber_queue_size=max(8, _env_int("AEGIS_RUNTIME_QUEUE_SIZE", 128)),
            grpc_max_workers=max(4, _env_int("AEGIS_RUNTIME_GRPC_WORKERS", 16)),
            shutdown_grace_sec=max(1.0, _env_float("AEGIS_RUNTIME_SHUTDOWN_GRACE_SEC", 5.0)),
        )
