"""Runtime 基础服务：gRPC ``AlgorithmRuntime`` 协议适配与进程托管入口（RuntimeServer / serve）。

职责说明（属于平台基础服务，算法只需在入口调用 ``serve(MyAlgorithm())``）：
- 实现 ``backend/api/proto/runtime/v1/runtime.proto`` 定义的 6 个 gRPC 接口：
  ``Health``、``AssignTask``、``UpdateTask``、``RevokeTask``、``RenewLease``、``StreamResults``；
- 进程启动时同步加载算法模型（``algorithm.load()``），加载完成后自动就绪；
- 监听 ``SIGINT`` / ``SIGTERM`` 实现优雅停机：先停止全部活跃任务释放单路资源，
  再调用 ``algorithm.unload()`` 释放全局模型显存，最后优雅关闭 gRPC Server。
"""

from __future__ import annotations

import json
import logging
import queue
import signal
import threading
from concurrent import futures

import grpc

from aegis_runtime_sdk.contract.algorithm import BaseAlgorithm
from aegis_runtime_sdk.contract.models import Point, ROI, TaskContext
from aegis_runtime_sdk.pb import runtime_pb2, runtime_pb2_grpc
from aegis_runtime_sdk.runtime.config import RuntimeConfig
from aegis_runtime_sdk.runtime.result_bus import ResultBus
from aegis_runtime_sdk.runtime.stream import FrameSourceFactory
from aegis_runtime_sdk.runtime.task_manager import TaskManager

logger = logging.getLogger("aegis.runtime.server")


def _parse_rois(pb_rois) -> list[ROI]:
    return [
        ROI(
            roi_id=r.roi_id,
            name=r.name,
            roi_type=r.roi_type,
            points=[Point(x=p.x, y=p.y) for p in r.points],
        )
        for r in pb_rois
    ]


def _parse_params(raw_json: str) -> dict:
    if not raw_json:
        return {}
    try:
        parsed = json.loads(raw_json)
        return parsed if isinstance(parsed, dict) else {}
    except Exception:
        return {}


class RuntimeServer(runtime_pb2_grpc.AlgorithmRuntimeServicer):
    """``aegis.runtime.v1.AlgorithmRuntime`` 的 gRPC 协议适配器。

    内部将业务编排委托给 :class:`TaskManager`、:class:`ResultBus` 与 :class:`LeaseWatchdog`，
    本身仅负责 Protobuf 请求解码、健康状态汇报与流式结果响应。
    """

    def __init__(
        self,
        runtime_key: str | None = None,
        algorithm: BaseAlgorithm | None = None,
        *,
        config: RuntimeConfig | None = None,
        version: str | None = None,
        heartbeat_interval_sec: float | None = None,
        watchdog_interval_sec: float | None = None,
        infer_interval_sec: float | None = None,
        max_tasks: int | None = None,
        frame_source_factory: FrameSourceFactory | None = None,
    ) -> None:
        if algorithm is None:
            raise ValueError("algorithm instance is required")

        meta = algorithm.metadata()
        resolved_cfg = config or RuntimeConfig(
            runtime_key=runtime_key or meta.runtime_key or "py-default",
            version=version or meta.version or "0.1.0",
        )
        if runtime_key is not None:
            resolved_cfg.runtime_key = runtime_key
        if version is not None:
            resolved_cfg.version = version
        if heartbeat_interval_sec is not None:
            resolved_cfg.heartbeat_interval_sec = max(0.05, heartbeat_interval_sec)
        if watchdog_interval_sec is not None:
            resolved_cfg.watchdog_interval_sec = max(0.02, watchdog_interval_sec)
        if infer_interval_sec is not None:
            resolved_cfg.default_infer_interval_sec = max(0.01, infer_interval_sec)
        if max_tasks is not None:
            resolved_cfg.max_tasks = max(0, max_tasks)

        self._config = resolved_cfg
        self._algorithm = algorithm
        self._ready = False
        self._closed = threading.Event()
        self._unloaded = False

        self._bus = ResultBus(subscriber_queue_size=resolved_cfg.subscriber_queue_size)

        # 启动阶段先加载模型权重，加载成功后再标记就绪并启动看门狗与任务管理器
        self._algorithm.load()
        self._tasks = TaskManager(
            algorithm=algorithm,
            result_bus=self._bus,
            config=resolved_cfg,
            frame_source_factory=frame_source_factory,
        )
        self._ready = True

    @property
    def config(self) -> RuntimeConfig:
        """返回当前生效的 :class:`RuntimeConfig`。"""
        return self._config

    def Health(self, request, context):  # noqa: N802
        algo_healthy = False
        if self._ready and not self._closed.is_set():
            try:
                algo_healthy = bool(self._algorithm.health_check())
            except Exception:
                logger.exception("algorithm.health_check() raised an exception")
                algo_healthy = False
        return runtime_pb2.HealthResponse(
            ready=algo_healthy,
            runtime_key=self._config.runtime_key,
            version=self._config.version,
            active_tasks=self._tasks.active_task_count(),
        )

    def AssignTask(self, request, context):  # noqa: N802
        task_ctx = TaskContext(
            task_id=request.task_id,
            camera_id=request.camera_id,
            algorithm_version_id=request.algorithm_version_id,
            stream_url=request.stream_url,
            stream_protocol=request.stream_protocol or "rtsp",
            rois=_parse_rois(request.rois),
            algo_params=_parse_params(request.algo_params_json),
            lease_id=request.lease_id,
            lease_ttl_seconds=request.lease_ttl_seconds or 30,
            config_version=request.config_version,
        )
        accepted, err_msg = self._tasks.assign_task(task_ctx)
        return runtime_pb2.RuntimeTaskReply(accepted=accepted, error_message=err_msg)

    def UpdateTask(self, request, context):  # noqa: N802
        accepted, err_msg = self._tasks.update_task(
            request.task_id,
            rois=_parse_rois(request.rois),
            algo_params=_parse_params(request.algo_params_json),
            config_version=request.config_version,
        )
        return runtime_pb2.RuntimeTaskReply(accepted=accepted, error_message=err_msg)

    def RevokeTask(self, request, context):  # noqa: N802
        self._tasks.stop_task(request.task_id, reason=request.reason or "revoked")
        return runtime_pb2.RuntimeTaskReply(accepted=True)

    def RenewLease(self, request, context):  # noqa: N802
        accepted, err_msg = self._tasks.renew_lease(
            request.task_id,
            lease_id=request.lease_id,
            lease_ttl_seconds=request.lease_ttl_seconds or 30,
            refreshed_stream_url=request.refreshed_stream_url,
        )
        return runtime_pb2.RuntimeTaskReply(accepted=accepted, error_message=err_msg)

    def StreamResults(self, request, context):  # noqa: N802
        sub_q = self._bus.subscribe()
        try:
            while context.is_active() and not self._closed.is_set():
                try:
                    item = sub_q.get(timeout=0.25)
                    yield item
                except queue.Empty:
                    continue
        finally:
            self._bus.unsubscribe(sub_q)

    def shutdown(self) -> None:
        """优雅停机：先停止所有活跃任务释放单路资源，再调用 ``algorithm.unload()`` 释放全局显存。"""
        self._closed.set()
        self._ready = False
        self._tasks.shutdown()
        if not self._unloaded:
            self._unloaded = True
            try:
                self._algorithm.unload()
            except Exception:
                logger.exception("algorithm.unload() failed during runtime shutdown")


def serve(
    algorithm: BaseAlgorithm,
    *,
    runtime_key: str | None = None,
    addr: str | None = None,
    version: str | None = None,
    config: RuntimeConfig | None = None,
    frame_source_factory: FrameSourceFactory | None = None,
) -> None:
    """启动阻塞式 AegisVision Python Runtime gRPC 服务（算法进程的标准主入口）。

    所有基础服务参数均支持通过 ``AEGIS_RUNTIME_*`` 环境变量注入（由 ``aegis-agent`` 自动传入），
    也可通过 ``config`` 或关键字参数覆盖。

    Args:
        algorithm: 算法工程师实现的 :class:`FrameAlgorithm` 或 :class:`BaseAlgorithm` 实例。
        runtime_key: 可选覆盖 ``AEGIS_RUNTIME_KEY``。
        addr: 可选覆盖 ``AEGIS_RUNTIME_ADDR``（默认 ``127.0.0.1:50051``）。
        version: 可选覆盖版本号（未传时优先取 ``algorithm.metadata().version``）。
        config: 可选传入完整的 :class:`RuntimeConfig`。
        frame_source_factory: 可选覆盖默认 ``OpenCVStreamSource`` 拉流工厂（如接入硬件解码器）。

    Example::

        if __name__ == "__main__":
            serve(MyHelmetAlgorithm(), runtime_key="py-helmet-v1")
    """
    meta = algorithm.metadata()
    cfg = config or RuntimeConfig.from_env(
        runtime_key=runtime_key or meta.runtime_key or None,
        bind_addr=addr,
        version=version or meta.version or None,
    )

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=cfg.grpc_max_workers))
    rt = RuntimeServer(
        algorithm=algorithm,
        config=cfg,
        frame_source_factory=frame_source_factory,
    )
    runtime_pb2_grpc.add_AlgorithmRuntimeServicer_to_server(rt, server)
    server.add_insecure_port(cfg.bind_addr)
    server.start()
    logger.info(
        "AegisVision Python Runtime %s (v%s) listening on %s",
        cfg.runtime_key,
        cfg.version,
        cfg.bind_addr,
    )

    stop_event = threading.Event()

    def _handle_sig(_signum, _frame):
        stop_event.set()

    signal.signal(signal.SIGINT, _handle_sig)
    signal.signal(signal.SIGTERM, _handle_sig)

    stop_event.wait()
    logger.info("Stopping AegisVision Python Runtime %s ...", cfg.runtime_key)
    rt.shutdown()
    server.stop(grace=cfg.shutdown_grace_sec)
