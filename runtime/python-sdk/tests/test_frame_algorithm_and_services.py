"""FrameAlgorithm 帧级推理模式与 Runtime 基础服务（拉流、心跳防误报、自动抓拍、热更新、容量限制、队列背压）测试。"""

from __future__ import annotations

import threading
import time
from concurrent import futures

import grpc

from aegis_runtime_sdk.contract import (
    AlgorithmMetadata,
    Detection,
    Frame,
    FrameAlgorithm,
    FrameOutput,
    TaskContext,
)
from aegis_runtime_sdk.pb import runtime_pb2, runtime_pb2_grpc
from aegis_runtime_sdk.runtime import FrameSource, ResultBus, RuntimeConfig, RuntimeServer


class FakeFrameSource(FrameSource):
    """可控测试帧源：支持正常出帧、模拟断流（返回 None）、记录 URL 刷新与关闭调用。"""

    def __init__(self, *, disconnected: bool = False) -> None:
        self.disconnected = disconnected
        self.opened_urls: list[str] = []
        self.updated_urls: list[str] = []
        self.closed = False
        self._frame_idx = 0

    def open(self, stream_url: str) -> None:
        self.opened_urls.append(stream_url)

    def update_url(self, stream_url: str) -> None:
        self.updated_urls.append(stream_url)

    def read(self, stop_event: threading.Event) -> Frame | None:
        if self.disconnected or stop_event.is_set():
            return None
        self._frame_idx += 1
        return Frame(
            image={"frame": self._frame_idx},
            width=1920,
            height=1080,
            timestamp_ms=int(time.time() * 1000),
            frame_index=self._frame_idx,
            _jpeg_encoder=lambda _img, q: f"fake-jpeg-q{q}".encode(),
        )

    def close(self) -> None:
        self.closed = True


class SampleFrameAlgo(FrameAlgorithm):
    """最小帧级算法实现：验证自动抓拍、热更新、健康检查与 unload 生命周期。"""

    auto_snapshot_on_abnormal = True
    snapshot_jpeg_quality = 80

    def __init__(self, *, emit_abnormal: bool = True) -> None:
        self.emit_abnormal = emit_abnormal
        self.loaded = False
        self.unloaded = False
        self.healthy = True
        self.updated_versions: list[int] = []
        self.updated_urls: list[str] = []
        self.last_required_labels: frozenset[str] = frozenset()
        self.stopped_tasks: list[str] = []

    def metadata(self) -> AlgorithmMetadata:
        return AlgorithmMetadata(
            runtime_key="py-frame-test",
            version="2.0.0",
            supported_labels=("no_helmet",),
        )

    def load(self) -> None:
        self.loaded = True

    def unload(self) -> None:
        self.unloaded = True

    def health_check(self) -> bool:
        return self.healthy

    def on_task_start(self, task_ctx: TaskContext) -> None:
        self.last_required_labels = task_ctx.required_labels

    def on_task_update(self, task_ctx: TaskContext) -> None:
        self.updated_versions.append(task_ctx.config_version)
        self.updated_urls.append(task_ctx.stream_url)
        self.last_required_labels = task_ctx.required_labels

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        self.stopped_tasks.append(task_ctx.task_id)

    def infer_frame(self, frame: Frame, task_ctx: TaskContext) -> list[Detection]:
        if not self.emit_abnormal:
            return []
        return [
            Detection.from_xyxy_pixels(
                192,
                108,
                960,
                540,
                frame.width,
                frame.height,
                label="no_helmet",
                confidence=0.95,
                track_id="trk-1",
                is_abnormal=True,
            )
        ]


def _launch_server(
    algo: FrameAlgorithm,
    sources: list[FakeFrameSource],
    *,
    disconnected: bool = False,
    max_tasks: int = 0,
    heartbeat_interval_sec: float = 0.15,
):
    def factory(_ctx: TaskContext) -> FrameSource:
        src = FakeFrameSource(disconnected=disconnected)
        sources.append(src)
        return src

    cfg = RuntimeConfig(
        runtime_key="py-frame-test",
        version="2.0.0",
        heartbeat_interval_sec=heartbeat_interval_sec,
        watchdog_interval_sec=0.05,
        default_infer_interval_sec=0.05,
        max_tasks=max_tasks,
    )
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    rt = RuntimeServer(algorithm=algo, config=cfg, frame_source_factory=factory)
    runtime_pb2_grpc.add_AlgorithmRuntimeServicer_to_server(rt, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    channel = grpc.insecure_channel(f"127.0.0.1:{port}")
    stub = runtime_pb2_grpc.AlgorithmRuntimeStub(channel)
    return server, rt, channel, stub


def test_frame_algorithm_auto_snapshot_url_refresh_update_and_unload():
    algo = SampleFrameAlgo(emit_abnormal=True)
    sources: list[FakeFrameSource] = []
    server, rt, channel, stub = _launch_server(algo, sources, max_tasks=1)
    try:
        assert algo.loaded is True
        h = stub.Health(runtime_pb2.HealthRequest())
        assert h.ready is True and h.version == "2.0.0"

        # 分配第一个任务（包含两个不同目标行为的 ROI）
        r1 = stub.AssignTask(
            runtime_pb2.RuntimeAssignTaskRequest(
                task_id="task-f1",
                camera_id="cam-1",
                algorithm_version_id="ver-1",
                stream_url="rtsp://127.0.0.1:8554/cam1?token=old",
                stream_protocol="rtsp",
                rois=[
                    runtime_pb2.RuntimeROI(
                        roi_id="roi-1",
                        name="Safety",
                        roi_type="full_frame",
                        target_labels=["no_helmet"],
                    ),
                    runtime_pb2.RuntimeROI(
                        roi_id="roi-2",
                        name="Smoking",
                        roi_type="full_frame",
                        target_labels=["smoking"],
                    ),
                ],
                algo_params_json='{"target_fps": 20}',
                lease_id="lease-1",
                lease_ttl_seconds=10,
                config_version=1,
            )
        )
        assert r1.accepted is True
        assert algo.last_required_labels == frozenset({"no_helmet", "smoking"})
        assert len(sources) == 1
        assert sources[0].opened_urls == ["rtsp://127.0.0.1:8554/cam1?token=old"]

        # 超过 max_tasks=1 应拒绝新任务
        r2 = stub.AssignTask(
            runtime_pb2.RuntimeAssignTaskRequest(
                task_id="task-f2",
                camera_id="cam-2",
                algorithm_version_id="ver-1",
                stream_url="rtsp://127.0.0.1:8554/cam2",
                lease_id="lease-2",
                lease_ttl_seconds=10,
            )
        )
        assert r2.accepted is False
        assert "max_tasks" in r2.error_message

        # 订阅结果并验证自动填充宽高 + 自动 JPEG 抓拍编码
        stream = stub.StreamResults(
            runtime_pb2.StreamResultsRequest(runtime_key="py-frame-test"),
            timeout=3.0,
        )
        got = next(stream)
        assert got.task_id == "task-f1"
        assert got.is_heartbeat is False
        assert got.frame_width == 1920 and got.frame_height == 1080
        assert len(got.detections) == 1
        assert got.detections[0].label == "no_helmet"
        assert got.snapshot_jpeg == b"fake-jpeg-q80"

        # 验证 UpdateTask 热更新回调（含 ROI target_labels 更新）
        upd = stub.UpdateTask(
            runtime_pb2.RuntimeUpdateTaskRequest(
                task_id="task-f1",
                rois=[
                    runtime_pb2.RuntimeROI(
                        roi_id="roi-1",
                        name="Safety",
                        roi_type="full_frame",
                        target_labels=["fall", "no_helmet"],
                    ),
                ],
                algo_params_json='{"target_fps": 15, "min_confidence": 0.7}',
                config_version=2,
            )
        )
        assert upd.accepted is True
        assert 2 in algo.updated_versions
        assert algo.last_required_labels == frozenset({"fall", "no_helmet"})

        # 验证 RenewLease 携带新流地址时同时通知 FrameSource 与 Algorithm
        ren = stub.RenewLease(
            runtime_pb2.RuntimeRenewLeaseRequest(
                task_id="task-f1",
                lease_id="lease-1",
                lease_ttl_seconds=10,
                refreshed_stream_url="rtsp://127.0.0.1:8554/cam1?token=fresh",
            )
        )
        assert ren.accepted is True
        assert sources[0].updated_urls == ["rtsp://127.0.0.1:8554/cam1?token=fresh"]
        assert "rtsp://127.0.0.1:8554/cam1?token=fresh" in algo.updated_urls

        # 验证算法 health_check() 返回 False 时 gRPC Health 报告未就绪
        algo.healthy = False
        assert stub.Health(runtime_pb2.HealthRequest()).ready is False
    finally:
        rt.shutdown()
        channel.close()
        server.stop(grace=1)

    assert "task-f1" in algo.stopped_tasks
    assert sources[0].closed is True
    assert algo.unloaded is True


def test_frame_algorithm_does_not_emit_false_heartbeats_when_stream_disconnected():
    """核心红线 #6：断流（FrameSource.read 返回 None）期间绝不发空帧心跳，防止离岗检测误报。"""
    algo = SampleFrameAlgo(emit_abnormal=False)
    sources: list[FakeFrameSource] = []
    server, rt, channel, stub = _launch_server(
        algo,
        sources,
        disconnected=True,
        heartbeat_interval_sec=0.1,
    )
    try:
        stub.AssignTask(
            runtime_pb2.RuntimeAssignTaskRequest(
                task_id="task-offline",
                camera_id="cam-off",
                algorithm_version_id="ver-1",
                stream_url="rtsp://127.0.0.1:8554/offline",
                lease_id="lease-off",
                lease_ttl_seconds=10,
            )
        )

        # 断流 0.35s 期间不应产生任何心跳帧
        sub_q = rt._bus.subscribe()
        try:
            time.sleep(0.35)
            assert sub_q.empty() is True

            # 流恢复后（画面无目标），应立即开始正常发送空帧心跳
            sources[0].disconnected = False
            msg = sub_q.get(timeout=1.5)
            assert msg.task_id == "task-offline"
            assert msg.is_heartbeat is True
            assert len(msg.detections) == 0
        finally:
            rt._bus.unsubscribe(sub_q)
    finally:
        rt.shutdown()
        channel.close()
        server.stop(grace=1)


def test_result_bus_drops_oldest_on_slow_subscriber():
    bus = ResultBus(subscriber_queue_size=8)
    sub_q = bus.subscribe()
    ctx = TaskContext(
        task_id="t-bus",
        camera_id="c-bus",
        algorithm_version_id="v-bus",
        stream_url="rtsp://127.0.0.1/bus",
        stream_protocol="rtsp",
        rois=[],
        algo_params={},
        lease_id="l",
        lease_ttl_seconds=30,
        config_version=1,
    )
    for i in range(12):
        bus.publish_output(
            ctx,
            FrameOutput(
                detections=[Detection(bbox=(0.1, 0.1, 0.2, 0.2), label=f"lbl-{i}", confidence=0.9)],
                frame_width=640,
                frame_height=360,
            ),
            is_heartbeat=False,
            timestamp_ms=1000 + i,
        )

    items = []
    while not sub_q.empty():
        items.append(sub_q.get_nowait())
    assert len(items) == 8
    # 丢旧保新：保留最新的 8 条（lbl-4 .. lbl-11）
    assert items[0].detections[0].label == "lbl-4"
    assert items[-1].detections[0].label == "lbl-11"


def test_example_yolo_style_algorithm_smoke():
    from examples.yolo_style_algorithm import HelmetDetectorAlgorithm

    algo = HelmetDetectorAlgorithm()
    algo.load()
    assert algo.health_check() is True

    ctx = TaskContext(
        task_id="t-ex",
        camera_id="c-ex",
        algorithm_version_id="v-ex",
        stream_url="rtsp://127.0.0.1/ex",
        stream_protocol="rtsp",
        rois=[],
        algo_params={"min_confidence": 0.5},
        lease_id="l-ex",
        lease_ttl_seconds=30,
        config_version=1,
    )
    algo.on_task_start(ctx)
    frame = Frame(image=object(), width=1920, height=1080, timestamp_ms=1, frame_index=1)
    dets = algo.infer_frame(frame, ctx)
    assert len(dets) == 1
    assert dets[0].label == "no_helmet"
    assert dets[0].is_abnormal is True
    assert 0.0 <= dets[0].bbox[0] < dets[0].bbox[2] <= 1.0
    algo.on_task_stop(ctx)
    algo.unload()
    assert algo.health_check() is False
