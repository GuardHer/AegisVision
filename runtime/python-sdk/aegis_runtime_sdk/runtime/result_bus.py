"""Runtime 基础服务：检测结果有界分发总线与 Protobuf 序列化器（ResultBus）。

职责说明（属于平台基础服务，算法无需关心）：
- 将算法输出的 :class:`FrameOutput` 安全序列化为 gRPC ``runtime_pb2.RuntimeResult``；
- 防御性裁剪检测框坐标至 ``[0.0, 1.0]`` 与置信度至 ``[0.0, 1.0]``，避免越界脏数据污染前端 Canvas 与规则引擎；
- 维护多订阅者有界内存队列，当消费端阻塞（慢客户端）时自动**丢旧保新**，严禁反向拖慢推理线程。
"""

from __future__ import annotations

import queue
import threading
import time

from aegis_runtime_sdk.contract.models import FrameOutput, TaskContext
from aegis_runtime_sdk.pb import runtime_pb2


class ResultBus:
    """线程安全的检测结果有界分发总线。"""

    def __init__(self, subscriber_queue_size: int = 128) -> None:
        self._queue_size = max(8, int(subscriber_queue_size))
        self._lock = threading.Lock()
        self._subscribers: list[queue.Queue[runtime_pb2.RuntimeResult]] = []

    def subscribe(self) -> queue.Queue[runtime_pb2.RuntimeResult]:
        """注册一个新的 ``StreamResults`` 订阅者并返回其有界队列。"""
        sub_q: queue.Queue[runtime_pb2.RuntimeResult] = queue.Queue(maxsize=self._queue_size)
        with self._lock:
            self._subscribers.append(sub_q)
        return sub_q

    def unsubscribe(self, sub_q: queue.Queue[runtime_pb2.RuntimeResult]) -> None:
        """注销订阅者队列。"""
        with self._lock:
            if sub_q in self._subscribers:
                self._subscribers.remove(sub_q)

    def publish_output(
        self,
        task_ctx: TaskContext,
        output: FrameOutput,
        *,
        is_heartbeat: bool,
        timestamp_ms: int | None = None,
    ) -> runtime_pb2.RuntimeResult:
        """构建 Protobuf ``RuntimeResult`` 并广播至所有在线订阅者（慢客户端丢旧保新）。"""
        result = self.build_proto_result(
            task_ctx,
            output,
            is_heartbeat=is_heartbeat,
            timestamp_ms=timestamp_ms,
        )
        self.publish(result)
        return result

    def publish(self, result: runtime_pb2.RuntimeResult) -> None:
        """向全部订阅者广播已构造的 ``RuntimeResult``。"""
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

    @staticmethod
    def build_proto_result(
        task_ctx: TaskContext,
        output: FrameOutput,
        *,
        is_heartbeat: bool,
        timestamp_ms: int | None = None,
    ) -> runtime_pb2.RuntimeResult:
        """将契约层的 :class:`FrameOutput` 转换为 gRPC ``RuntimeResult`` 消息。"""
        pb_dets = [
            runtime_pb2.RuntimeDetection(
                bbox=list(d.clamped_bbox()),
                label=str(d.label),
                confidence=max(0.0, min(1.0, float(d.confidence))),
                track_id=str(d.track_id or ""),
                is_abnormal=bool(d.is_abnormal),
                attributes={str(k): str(v) for k, v in (d.attributes or {}).items()},
            )
            for d in output.detections
        ]
        return runtime_pb2.RuntimeResult(
            task_id=task_ctx.task_id,
            camera_id=task_ctx.camera_id,
            algorithm_version_id=task_ctx.algorithm_version_id,
            timestamp_ms=timestamp_ms if timestamp_ms is not None else int(time.time() * 1000),
            frame_width=int(output.frame_width),
            frame_height=int(output.frame_height),
            is_heartbeat=is_heartbeat,
            detections=pb_dets,
            snapshot_jpeg=output.snapshot_jpeg or b"",
        )
