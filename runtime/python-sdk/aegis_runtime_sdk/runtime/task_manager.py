"""Runtime 基础服务：多路推理任务调度与执行管理器（TaskManager）。

职责说明（属于平台基础服务，算法无需关心）：
1. **任务生命周期托管**：统一调度 ``AssignTask`` / ``UpdateTask`` / ``RenewLease`` / ``RevokeTask``，
   按 ``task_id`` 维护独立工作线程，并在停止时确保工作线程退出后再回调 ``algorithm.on_task_stop``；
2. **双模式透明驱动**：
   - 若算法继承 :class:`FrameAlgorithm`，自动通过 :class:`FrameSource` 拉流解码并调用 ``infer_frame(frame, ctx)``，
     同时在异常帧缺失 ``snapshot_jpeg`` 时自动调用 ``frame.encode_jpeg()`` 补齐证据抓拍图；
   - 若算法继承 :class:`BaseAlgorithm`，直接驱动 ``infer_task(ctx)``；
3. **动态控频**：优先按任务配置 ``algo_params["target_fps"]`` 控制推理节拍，未配置时回退至默认间隔；
4. **离岗检测防误报（红线保障）**：
   - 当拉流成功且画面无目标时，严格按 ``heartbeat_interval_sec`` 发送 ``is_heartbeat=True`` 空帧心跳；
   - 当拉流断流（``FrameSource.read()`` 返回 ``None``）时，**不发送**虚假空帧心跳，使平台侧离岗计时自动暂停；
5. **异常隔离与容量保护**：单路任务推理异常只记录日志不波及其他摄像头，可选强制校验 ``max_tasks`` 上限。
"""

from __future__ import annotations

import logging
import threading
import time
from dataclasses import dataclass

from aegis_runtime_sdk.contract.algorithm import BaseAlgorithm, FrameAlgorithm
from aegis_runtime_sdk.contract.models import Detection, Frame, FrameOutput, ROI, TaskContext
from aegis_runtime_sdk.runtime.config import RuntimeConfig
from aegis_runtime_sdk.runtime.lease import LeaseWatchdog
from aegis_runtime_sdk.runtime.result_bus import ResultBus
from aegis_runtime_sdk.runtime.stream import FrameSource, FrameSourceFactory, OpenCVStreamSource

logger = logging.getLogger("aegis.runtime.tasks")


@dataclass
class _RunningTask:
    ctx: TaskContext
    worker: threading.Thread
    source: FrameSource | None = None


class TaskManager:
    """多路摄像头推理任务调度器（Runtime 核心基础服务）。"""

    def __init__(
        self,
        algorithm: BaseAlgorithm,
        result_bus: ResultBus,
        config: RuntimeConfig,
        *,
        frame_source_factory: FrameSourceFactory | None = None,
    ) -> None:
        self._algorithm = algorithm
        self._bus = result_bus
        self._config = config
        self._frame_source_factory: FrameSourceFactory = frame_source_factory or (
            lambda _ctx: OpenCVStreamSource()
        )

        self._lock = threading.Lock()
        self._tasks: dict[str, _RunningTask] = {}
        self._closed = threading.Event()

        self._watchdog = LeaseWatchdog(
            on_expire=self._handle_lease_expired,
            check_interval_sec=config.watchdog_interval_sec,
        )

    def active_task_count(self) -> int:
        """返回当前运行中的活跃任务数。"""
        with self._lock:
            return len(self._tasks)

    def assign_task(self, task_ctx: TaskContext) -> tuple[bool, str]:
        """分配并启动一个新推理任务；若同 ``task_id`` 已存在则先安全停止再重建。"""
        if self._closed.is_set():
            return False, "runtime is shutting down"
        if not task_ctx.task_id:
            return False, "task_id is required"

        with self._lock:
            already_exists = task_ctx.task_id in self._tasks
            current_count = len(self._tasks)
        if already_exists:
            self.stop_task(task_ctx.task_id, reason="reassigned")
        elif self._config.max_tasks > 0 and current_count >= self._config.max_tasks:
            return False, f"runtime reached max_tasks limit ({self._config.max_tasks})"

        source: FrameSource | None = None
        if isinstance(self._algorithm, FrameAlgorithm):
            source = self._frame_source_factory(task_ctx)
            try:
                source.open(task_ctx.stream_url)
            except Exception as exc:
                source.close()
                return False, f"open stream failed: {exc}"

        try:
            self._algorithm.on_task_start(task_ctx)
        except Exception as exc:
            if source is not None:
                source.close()
            return False, str(exc)

        worker = threading.Thread(
            target=self._task_loop,
            args=(task_ctx, source),
            name=f"aegis-task-{task_ctx.task_id[:8]}",
            daemon=True,
        )
        with self._lock:
            self._tasks[task_ctx.task_id] = _RunningTask(ctx=task_ctx, worker=worker, source=source)

        self._watchdog.register(task_ctx.task_id, task_ctx.lease_id, task_ctx.lease_ttl_seconds)
        worker.start()
        return True, ""

    def update_task(
        self,
        task_id: str,
        *,
        rois: list[ROI],
        algo_params: dict,
        config_version: int,
    ) -> tuple[bool, str]:
        """热更新活跃任务的 ROI、超参数与配置版本号。"""
        with self._lock:
            running = self._tasks.get(task_id)
            if running is None:
                return False, "task not found"
            running.ctx.rois = rois
            running.ctx.algo_params = algo_params
            running.ctx.config_version = config_version
            task_ctx = running.ctx

        try:
            self._algorithm.on_task_update(task_ctx)
        except Exception as exc:
            return False, str(exc)
        return True, ""

    def renew_lease(
        self,
        task_id: str,
        *,
        lease_id: str,
        lease_ttl_seconds: int,
        refreshed_stream_url: str = "",
    ) -> tuple[bool, str]:
        """续期任务租约，并在携带新短期鉴权流地址时无缝通知拉流源与算法。"""
        ttl = lease_ttl_seconds or 30
        ok = self._watchdog.renew(task_id, lease_id, ttl)
        if not ok:
            return False, "task lease not found"

        notify_ctx: TaskContext | None = None
        notify_source: FrameSource | None = None
        with self._lock:
            running = self._tasks.get(task_id)
            if running is not None:
                running.ctx.lease_id = lease_id
                running.ctx.lease_ttl_seconds = ttl
                if refreshed_stream_url and refreshed_stream_url != running.ctx.stream_url:
                    running.ctx.stream_url = refreshed_stream_url
                    notify_ctx = running.ctx
                    notify_source = running.source

        if notify_source is not None:
            try:
                notify_source.update_url(refreshed_stream_url)
            except Exception:
                logger.exception("FrameSource.update_url failed for task %s", task_id)

        if notify_ctx is not None:
            try:
                self._algorithm.on_task_update(notify_ctx)
            except Exception:
                logger.exception("on_task_update failed after stream URL refresh for task %s", task_id)

        return True, ""

    def stop_task(self, task_id: str, *, reason: str) -> None:
        """停止指定任务：注销租约 -> 置位停止事件 -> 等待工作线程退出 -> 关闭拉流源 -> 回调 ``on_task_stop``。"""
        self._watchdog.unregister(task_id)
        with self._lock:
            running = self._tasks.pop(task_id, None)
        if running is None:
            return

        running.ctx.stop_event.set()
        if running.worker.is_alive() and running.worker is not threading.current_thread():
            running.worker.join(timeout=2.0)

        if running.source is not None:
            try:
                running.source.close()
            except Exception:
                logger.exception("FrameSource.close failed for task %s (reason=%s)", task_id, reason)

        try:
            self._algorithm.on_task_stop(running.ctx)
        except Exception:
            logger.exception("on_task_stop failed for task %s (reason=%s)", task_id, reason)

    def shutdown(self) -> None:
        """停止看门狗并优雅停止全部活跃任务。"""
        self._closed.set()
        self._watchdog.stop()
        with self._lock:
            task_ids = list(self._tasks.keys())
        for tid in task_ids:
            self.stop_task(tid, reason="runtime shutdown")

    def _handle_lease_expired(self, task_id: str) -> None:
        logger.warning("Lease expired for task %s; auto-stopping to release GPU resources", task_id)
        self.stop_task(task_id, reason="lease expired")

    def _compute_interval(self, task_ctx: TaskContext) -> float:
        fps = task_ctx.target_fps
        if fps is not None and fps > 0:
            return max(0.01, 1.0 / fps)
        return max(0.01, self._config.default_infer_interval_sec)

    def _task_loop(self, task_ctx: TaskContext, source: FrameSource | None) -> None:
        last_emit = 0.0
        is_frame_algo = isinstance(self._algorithm, FrameAlgorithm)

        while not task_ctx.stop_event.is_set() and not self._closed.is_set():
            loop_start = time.monotonic()

            if is_frame_algo:
                assert isinstance(self._algorithm, FrameAlgorithm)
                last_emit = self._step_frame_algorithm(task_ctx, source, self._algorithm, last_emit)
            else:
                last_emit = self._step_task_algorithm(task_ctx, last_emit)

            interval = self._compute_interval(task_ctx)
            elapsed = time.monotonic() - loop_start
            remaining = interval - elapsed
            if remaining > 0:
                task_ctx.stop_event.wait(remaining)

    def _step_frame_algorithm(
        self,
        task_ctx: TaskContext,
        source: FrameSource | None,
        algo: FrameAlgorithm,
        last_emit: float,
    ) -> float:
        if source is None:
            return last_emit
        try:
            frame = source.read(task_ctx.stop_event)
        except Exception:
            logger.exception("FrameSource.read error on task %s", task_ctx.task_id)
            return last_emit

        # 拉流失败或断流重连期间不发射虚假空帧心跳，防止离岗检测误报
        if frame is None:
            return last_emit

        try:
            raw_out = algo.infer_frame(frame, task_ctx)
        except Exception:
            logger.exception("infer_frame error on task %s", task_ctx.task_id)
            return last_emit

        output = self._normalize_frame_output(raw_out, frame, algo)
        if output is None:
            return last_emit

        now = time.monotonic()
        if output.has_detections:
            self._bus.publish_output(
                task_ctx,
                output,
                is_heartbeat=False,
                timestamp_ms=frame.timestamp_ms,
            )
            return now

        if now - last_emit >= self._config.heartbeat_interval_sec:
            self._bus.publish_output(
                task_ctx,
                output,
                is_heartbeat=True,
                timestamp_ms=frame.timestamp_ms,
            )
            return now
        return last_emit

    def _step_task_algorithm(self, task_ctx: TaskContext, last_emit: float) -> float:
        now = time.monotonic()
        try:
            output = self._algorithm.infer_task(task_ctx)
        except Exception:
            logger.exception("infer_task error on task %s", task_ctx.task_id)
            output = None

        if output is None:
            return last_emit

        if output.has_detections:
            self._bus.publish_output(task_ctx, output, is_heartbeat=False)
            return now

        if now - last_emit >= self._config.heartbeat_interval_sec:
            self._bus.publish_output(task_ctx, output, is_heartbeat=True)
            return now
        return last_emit

    @staticmethod
    def _normalize_frame_output(
        raw_out: list[Detection] | FrameOutput | None,
        frame: Frame,
        algo: FrameAlgorithm,
    ) -> FrameOutput | None:
        if raw_out is None:
            return None
        if isinstance(raw_out, FrameOutput):
            output = raw_out
            if output.frame_width <= 0:
                output.frame_width = frame.width
            if output.frame_height <= 0:
                output.frame_height = frame.height
        else:
            output = FrameOutput(
                detections=list(raw_out),
                frame_width=frame.width,
                frame_height=frame.height,
            )

        need_snapshot = (
            not output.snapshot_jpeg
            and (
                (algo.auto_snapshot_on_abnormal and output.has_abnormal)
                or (algo.auto_snapshot_on_any_detection and output.has_detections)
            )
        )
        if need_snapshot:
            output.snapshot_jpeg = frame.encode_jpeg(quality=algo.snapshot_jpeg_quality)
        return output
