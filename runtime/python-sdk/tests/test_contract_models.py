"""契约层数据结构（aegis_runtime_sdk.contract.models）单元测试。"""

from __future__ import annotations

import threading
import pytest

from aegis_runtime_sdk.contract import (
    Detection,
    Frame,
    FrameOutput,
    Point,
    ROI,
    TaskContext,
)


def test_point_and_roi_geometry_helpers():
    pt = Point(x=0.5, y=0.25)
    assert pt.to_pixel(1920, 1080) == (960, 270)
    assert Point(x=-0.5, y=1.5).to_pixel(100, 200) == (0, 200)

    poly = ROI(
        roi_id="roi-1",
        name="Workstation-1",
        roi_type="polygon",
        points=[
            Point(0.2, 0.2),
            Point(0.8, 0.2),
            Point(0.8, 0.8),
            Point(0.2, 0.8),
        ],
    )
    assert poly.is_full_frame() is False
    assert poly.to_pixel_points(1000, 500) == [(200, 100), (800, 100), (800, 400), (200, 400)]
    assert poly.contains_point(0.5, 0.5) is True
    assert poly.contains_point(0.1, 0.1) is False

    full = ROI(roi_id="roi-full", name="Full", roi_type="full_frame", points=[])
    assert full.is_full_frame() is True
    assert full.to_pixel_points(640, 360) == [(0, 0), (640, 0), (640, 360), (0, 360)]
    assert full.contains_point(0.99, 0.99) is True


def test_detection_pixel_factories_and_clamping():
    det = Detection.from_xyxy_pixels(
        x1=960,
        y1=270,
        x2=1920,
        y2=1080,
        frame_width=1920,
        frame_height=1080,
        label="no_helmet",
        confidence=1.25,
        track_id="trk-7",
        is_abnormal=True,
        attributes={"color": "red"},
    )
    assert det.bbox == (0.5, 0.25, 1.0, 1.0)
    assert det.confidence == 1.0
    assert det.center() == Point(0.75, 0.625)
    assert det.bottom_center() == Point(0.75, 1.0)

    det_xywh = Detection.from_xywh_pixels(
        x=-50,
        y=100,
        w=550,
        h=300,
        frame_width=1000,
        frame_height=1000,
        label="person",
        confidence=0.8,
    )
    assert det_xywh.bbox == (0.0, 0.1, 0.5, 0.4)

    raw_oob = Detection(bbox=(1.2, -0.3, 0.4, 0.8), label="person", confidence=0.5)
    assert raw_oob.clamped_bbox() == (0.4, 0.0, 1.0, 0.8)

    with pytest.raises(ValueError):
        Detection.from_xyxy_pixels(0, 0, 10, 10, 0, 1080, label="x", confidence=0.5)


def test_frame_encode_and_task_context_helpers():
    frame = Frame(
        image={"dummy": True},
        width=1280,
        height=720,
        timestamp_ms=1700000000000,
        frame_index=1,
        _jpeg_encoder=lambda _img, q: f"jpeg-q{q}".encode(),
    )
    assert frame.encode_jpeg(90) == b"jpeg-q90"
    assert Frame(image=None, width=0, height=0, timestamp_ms=0).encode_jpeg() == b""

    out = FrameOutput(
        detections=[Detection(bbox=(0.1, 0.1, 0.2, 0.2), label="no_helmet", confidence=0.9, is_abnormal=True)],
        frame_width=1280,
        frame_height=720,
    )
    assert out.has_detections is True
    assert out.has_abnormal is True

    stop_ev = threading.Event()
    ctx = TaskContext(
        task_id="t-1",
        camera_id="c-1",
        algorithm_version_id="v-1",
        stream_url="rtsp://127.0.0.1:8554/cam1",
        stream_protocol="rtsp",
        rois=[],
        algo_params={"target_fps": 10, "min_confidence": 0.65},
        lease_id="lease-1",
        lease_ttl_seconds=30,
        config_version=1,
        stop_event=stop_ev,
    )
    assert ctx.target_fps == 10.0
    assert ctx.confidence_threshold == 0.65
    assert ctx.get_param("missing", "fallback") == "fallback"
    assert ctx.is_stopped() is False
    stop_ev.set()
    assert ctx.is_stopped() is True
    assert ctx.sleep(1.0) is True
