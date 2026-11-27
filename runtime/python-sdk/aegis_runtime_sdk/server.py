"""gRPC AlgorithmRuntime server supporting multi-task execution, empty heartbeats, and lease watchdog."""

from __future__ import annotations

import json
import logging
import os
import queue
import signal
import threading
import time
from concurrent import futures

import grpc

from aegis_runtime_sdk.base import BaseAlgorithm, FrameOutput, Point, ROI, TaskContext
from aegis_runtime_sdk.lease import LeaseWatchdog
from aegis_runtime_sdk.pb import runtime_pb2, runtime_pb2_grpc

logger = logging.getLogger("aegis.runtime")


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
    """Implements aegis.runtime.v1.AlgorithmRuntime with built-in lease watchdog and heartbeats."""

    def __init__(
        self,
        runtime_key: str,
        algorithm: BaseAlgorithm,
        version: str = "0.1.0",
        heartbeat_interval_sec: float = 1.0,
        watchdog_interval_sec: float = 0.25,
        infer_interval_sec: float = 0.2,
    ) -> None:
        self._runtime_key = runtime_key
        self._version = version
        self._algorithm = algorithm
        self._heartbeat_interval = max(0.05, heartbeat_interval_sec)
        self._infer_interval = max(0.02, infer_interval_sec)

        self._lock = threading.Lock()
        self._tasks: dict[str, TaskContext] = {}
        self._workers: dict[str, threading.Thread] = {}
        self._subscribers: list[queue.Queue[runtime_pb2.RuntimeResult]] = []
        self._ready = False
        self._closed = threading.Event()

        self._algorithm.load()
        self._ready = True
        self._watchdog = LeaseWatchdog(
            on_expire=self._handle_lease_expired,
            check_interval_sec=watchdog_interval_sec,
        )

    def Health(self, request, context):  # noqa: N802
        with self._lock:
            active = len(self._tasks)
        return runtime_pb2.HealthResponse(
            ready=self._ready and not self._closed.is_set(),
            runtime_key=self._runtime_key,
            version=self._version,
            active_tasks=active,
        )

    def AssignTask(self, request, context):  # noqa: N802
        task_id = request.task_id
        if not task_id:
            return runtime_pb2.RuntimeTaskReply(accepted=False, error_message="task_id is required")

        # Replace existing task cleanly if reassigned
        if task_id in self._tasks:
            self._stop_task(task_id, reason="reassigned")

        task_ctx = TaskContext(
            task_id=task_id,
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

        try:
            self._algorithm.on_task_start(task_ctx)
        except Exception as exc:
            return runtime_pb2.RuntimeTaskReply(accepted=False, error_message=str(exc))

        worker = threading.Thread(
            target=self._task_loop,
            args=(task_ctx,),
            name=f"aegis-task-{task_id[:8]}",
            daemon=True,
        )
        with self._lock:
            self._tasks[task_id] = task_ctx
            self._workers[task_id] = worker

        self._watchdog.register(task_id, task_ctx.lease_id, task_ctx.lease_ttl_seconds)
        worker.start()
        return runtime_pb2.RuntimeTaskReply(accepted=True)

    def UpdateTask(self, request, context):  # noqa: N802
        with self._lock:
            task_ctx = self._tasks.get(request.task_id)
            if task_ctx is None:
                return runtime_pb2.RuntimeTaskReply(accepted=False, error_message="task not found")
            task_ctx.rois = _parse_rois(request.rois)
            task_ctx.algo_params = _parse_params(request.algo_params_json)
            task_ctx.config_version = request.config_version
        try:
            self._algorithm.on_task_update(task_ctx)
        except Exception as exc:
            return runtime_pb2.RuntimeTaskReply(accepted=False, error_message=str(exc))
        return runtime_pb2.RuntimeTaskReply(accepted=True)

    def RevokeTask(self, request, context):  # noqa: N802
        self._stop_task(request.task_id, reason=request.reason or "revoked")
        return runtime_pb2.RuntimeTaskReply(accepted=True)

    def RenewLease(self, request, context):  # noqa: N802
        ok = self._watchdog.renew(request.task_id, request.lease_id, request.lease_ttl_seconds or 30)
        if not ok:
            return runtime_pb2.RuntimeTaskReply(accepted=False, error_message="task lease not found")
        with self._lock:
            task_ctx = self._tasks.get(request.task_id)
            if task_ctx is not None:
                task_ctx.lease_id = request.lease_id
                task_ctx.lease_ttl_seconds = request.lease_ttl_seconds or task_ctx.lease_ttl_seconds
                if request.refreshed_stream_url:
                    task_ctx.stream_url = request.refreshed_stream_url
                    try:
                        self._algorithm.on_task_update(task_ctx)
                    except Exception:
                        pass
        return runtime_pb2.RuntimeTaskReply(accepted=True)

    def StreamResults(self, request, context):  # noqa: N802
        sub_q: queue.Queue[runtime_pb2.RuntimeResult] = queue.Queue(maxsize=128)
        with self._lock:
            self._subscribers.append(sub_q)
        try:
            while context.is_active() and not self._closed.is_set():
                try:
                    item = sub_q.get(timeout=0.25)
                    yield item
                except queue.Empty:
                    continue
        finally:
            with self._lock:
                if sub_q in self._subscribers:
                    self._subscribers.remove(sub_q)

    def shutdown(self) -> None:
        self._closed.set()
        self._watchdog.stop()
        with self._lock:
            task_ids = list(self._tasks.keys())
        for tid in task_ids:
            self._stop_task(tid, reason="runtime shutdown")

    def _handle_lease_expired(self, task_id: str) -> None:
        logger.warning("Lease expired for task %s; auto-stopping to release resources", task_id)
        self._stop_task(task_id, reason="lease expired")

    def _stop_task(self, task_id: str, reason: str) -> None:
        self._watchdog.unregister(task_id)
        with self._lock:
            task_ctx = self._tasks.pop(task_id, None)
            worker = self._workers.pop(task_id, None)
        if task_ctx is None:
            return
        task_ctx.stop_event.set()
        if worker is not None and worker.is_alive() and worker is not threading.current_thread():
            worker.join(timeout=2.0)
        try:
            self._algorithm.on_task_stop(task_ctx)
        except Exception:
            logger.exception("on_task_stop failed for task %s (reason=%s)", task_id, reason)

    def _task_loop(self, task_ctx: TaskContext) -> None:
        last_emit = 0.0
        while not task_ctx.stop_event.is_set() and not self._closed.is_set():
            now = time.monotonic()
            try:
                output = self._algorithm.infer_task(task_ctx)
            except Exception:
                logger.exception("infer_task error on task %s", task_ctx.task_id)
                output = None

            has_detections = output is not None and len(output.detections) > 0
            if has_detections:
                self._publish(self._build_result(task_ctx, output, is_heartbeat=False))
                last_emit = now
            elif now - last_emit >= self._heartbeat_interval:
                # Guarantee empty heartbeat frame emission for accurate absence_detect timing.
                empty = output if output is not None else FrameOutput()
                self._publish(self._build_result(task_ctx, empty, is_heartbeat=True))
                last_emit = now

            task_ctx.stop_event.wait(self._infer_interval)

    def _build_result(
        self,
        task_ctx: TaskContext,
        output: FrameOutput,
        is_heartbeat: bool,
    ) -> runtime_pb2.RuntimeResult:
        pb_dets = [
            runtime_pb2.RuntimeDetection(
                bbox=list(d.bbox),
                label=d.label,
                confidence=float(d.confidence),
                track_id=d.track_id,
                is_abnormal=bool(d.is_abnormal),
                attributes=dict(d.attributes),
            )
            for d in output.detections
        ]
        return runtime_pb2.RuntimeResult(
            task_id=task_ctx.task_id,
            camera_id=task_ctx.camera_id,
            algorithm_version_id=task_ctx.algorithm_version_id,
            timestamp_ms=int(time.time() * 1000),
            frame_width=output.frame_width,
            frame_height=output.frame_height,
            is_heartbeat=is_heartbeat,
            detections=pb_dets,
            snapshot_jpeg=output.snapshot_jpeg or b"",
        )

    def _publish(self, result: runtime_pb2.RuntimeResult) -> None:
        with self._lock:
            subs = list(self._subscribers)
        for q in subs:
            try:
                q.put_nowait(result)
            except queue.Full:
                try:
                    q.get_nowait()
                except queue.Empty:
                    pass
                try:
                    q.put_nowait(result)
                except queue.Full:
                    pass


def serve(
    algorithm: BaseAlgorithm,
    runtime_key: str | None = None,
    addr: str | None = None,
    version: str = "0.1.0",
) -> None:
    """Start a blocking gRPC AlgorithmRuntime server configured via AEGIS_RUNTIME_* env vars."""
    key = runtime_key or os.getenv("AEGIS_RUNTIME_KEY", "py-default")
    bind_addr = addr or os.getenv("AEGIS_RUNTIME_ADDR", "127.0.0.1:50051")

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=16))
    rt = RuntimeServer(runtime_key=key, algorithm=algorithm, version=version)
    runtime_pb2_grpc.add_AlgorithmRuntimeServicer_to_server(rt, server)
    server.add_insecure_port(bind_addr)
    server.start()
    logger.info("AegisVision Python Runtime %s listening on %s", key, bind_addr)

    stop_event = threading.Event()

    def _handle_sig(_signum, _frame):
        stop_event.set()

    signal.signal(signal.SIGINT, _handle_sig)
    signal.signal(signal.SIGTERM, _handle_sig)

    stop_event.wait()
    rt.shutdown()
    server.stop(grace=5)
