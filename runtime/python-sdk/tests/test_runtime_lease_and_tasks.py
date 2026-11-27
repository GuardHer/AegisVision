import time
from concurrent import futures
import grpc
import pytest

from aegis_runtime_sdk.pb import runtime_pb2, runtime_pb2_grpc
from aegis_runtime_sdk.base import BaseAlgorithm, FrameOutput, TaskContext
from aegis_runtime_sdk.server import RuntimeServer


class StubEmptyAlgorithm(BaseAlgorithm):
    def __init__(self):
        self.started_tasks = set()
        self.stopped_tasks = set()

    def load(self) -> None:
        pass

    def on_task_start(self, task_ctx: TaskContext) -> None:
        self.started_tasks.add(task_ctx.task_id)

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        self.stopped_tasks.add(task_ctx.task_id)

    def infer_task(self, task_ctx: TaskContext) -> FrameOutput:
        # Simulate empty scene (no targets) to verify periodic empty heartbeat emission.
        return FrameOutput(detections=[], frame_width=1920, frame_height=1080)


def _start_test_server(algo: BaseAlgorithm, heartbeat_interval: float = 0.2, watchdog_interval: float = 0.1):
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    rt = RuntimeServer(
        runtime_key="py-mock-test",
        algorithm=algo,
        heartbeat_interval_sec=heartbeat_interval,
        watchdog_interval_sec=watchdog_interval,
        infer_interval_sec=0.1,
    )
    runtime_pb2_grpc.add_AlgorithmRuntimeServicer_to_server(rt, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    channel = grpc.insecure_channel(f"127.0.0.1:{port}")
    stub = runtime_pb2_grpc.AlgorithmRuntimeStub(channel)
    return server, rt, channel, stub


# Review Focus #3: When Agent crashes and RenewLease stops arriving past lease_ttl_seconds,
# Runtime MUST automatically stop the task and invoke stop() to release GPU VRAM.
def test_lease_expiry_auto_stops_task():
    algo = StubEmptyAlgorithm()
    server, rt, channel, stub = _start_test_server(algo, heartbeat_interval=0.2, watchdog_interval=0.05)
    try:
        reply = stub.AssignTask(
            runtime_pb2.RuntimeAssignTaskRequest(
                task_id="task-lease-1",
                camera_id="cam-1",
                algorithm_version_id="ver-1",
                stream_url="rtsp://127.0.0.1:8554/cam1?token=t",
                stream_protocol="rtsp",
                lease_id="lease-1",
                lease_ttl_seconds=1,
            )
        )
        assert reply.accepted is True
        health_before = stub.Health(runtime_pb2.HealthRequest())
        assert health_before.ready is True
        assert health_before.active_tasks == 1

        # Do NOT call RenewLease; wait past lease_ttl_seconds (1s)
        time.sleep(1.5)

        health_after = stub.Health(runtime_pb2.HealthRequest())
        assert health_after.active_tasks == 0
        assert "task-lease-1" in algo.stopped_tasks
    finally:
        rt.shutdown()
        channel.close()
        server.stop(grace=1)


def test_runtime_emits_empty_heartbeat_frames():
    algo = StubEmptyAlgorithm()
    server, rt, channel, stub = _start_test_server(algo, heartbeat_interval=0.15, watchdog_interval=0.1)
    try:
        stub.AssignTask(
            runtime_pb2.RuntimeAssignTaskRequest(
                task_id="task-hb-1",
                camera_id="cam-hb",
                algorithm_version_id="ver-hb",
                stream_url="rtsp://127.0.0.1:8554/cam_hb?token=t",
                stream_protocol="rtsp",
                lease_id="lease-hb",
                lease_ttl_seconds=10,
            )
        )

        stream = stub.StreamResults(
            runtime_pb2.StreamResultsRequest(runtime_key="py-mock-test"),
            timeout=3.0,
        )
        heartbeats = []
        for res in stream:
            if res.task_id == "task-hb-1" and res.is_heartbeat:
                assert len(res.detections) == 0
                heartbeats.append(res)
                if len(heartbeats) >= 2:
                    break
        assert len(heartbeats) >= 2
    finally:
        rt.shutdown()
        channel.close()
        server.stop(grace=1)
