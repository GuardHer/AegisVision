"""Base interfaces and data models for AegisVision Python algorithm runtimes."""

from __future__ import annotations

import threading
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class Point:
    x: float
    y: float


@dataclass(frozen=True)
class ROI:
    roi_id: str
    name: str
    roi_type: str
    points: list[Point] = field(default_factory=list)


@dataclass
class Detection:
    bbox: tuple[float, float, float, float]
    label: str
    confidence: float
    track_id: str = ""
    is_abnormal: bool = False
    attributes: dict[str, str] = field(default_factory=dict)


@dataclass
class FrameOutput:
    detections: list[Detection] = field(default_factory=list)
    frame_width: int = 0
    frame_height: int = 0
    snapshot_jpeg: bytes = b""


@dataclass
class TaskContext:
    task_id: str
    camera_id: str
    algorithm_version_id: str
    stream_url: str
    stream_protocol: str
    rois: list[ROI]
    algo_params: dict[str, Any]
    lease_id: str
    lease_ttl_seconds: int
    config_version: int
    stop_event: threading.Event = field(default_factory=threading.Event)

    def is_stopped(self) -> bool:
        return self.stop_event.is_set()


class BaseAlgorithm(ABC):
    """Abstract base class implemented by algorithm engineers.

    Implement `load()` to initialize model weights (PyTorch/ONNX/TensorRT) and
    `infer_task(task_ctx)` to execute one inference step for an assigned camera task.
    """

    @abstractmethod
    def load(self) -> None:
        """Load model weights and initialize inference resources once per Runtime process."""

    def on_task_start(self, task_ctx: TaskContext) -> None:
        """Optional hook called when a task is assigned (e.g. open/attach stream decoder)."""

    def on_task_update(self, task_ctx: TaskContext) -> None:
        """Optional hook called when ROIs, params, or refreshed stream_url change."""

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        """Optional hook called when a task is revoked or its lease expires (free stream/VRAM)."""

    @abstractmethod
    def infer_task(self, task_ctx: TaskContext) -> FrameOutput | None:
        """Produce detections (and optional snapshot_jpeg for abnormal frames) for one frame."""
