"""Runtime 基础服务：视频流拉流解码与断流重连抽象（FrameSource）。

职责说明（属于平台基础服务，帧级算法 ``FrameAlgorithm`` 无需关心）：
- 对接平台下发的授权内部低延迟 RTSP 地址（强制 TCP 传输，减少花屏丢包）；
- 当 ``RenewLease`` 携带刷新后的短期 Token ``refreshed_stream_url`` 时，无缝热切换底层连接；
- 断流时自动按指数退避重连，并在重连期间返回 ``None``（不伪造空帧心跳，避免 ``absence_detect`` 误报）；
- 可在单元测试或特殊硬件解码场景（如 PyAV / GStreamer / NVDEC）中注入自定义 ``FrameSourceFactory``。
"""

from __future__ import annotations

import logging
import os
import threading
import time
from abc import ABC, abstractmethod
from typing import Callable

from aegis_runtime_sdk.contract.models import Frame, TaskContext

logger = logging.getLogger("aegis.runtime.stream")


class FrameSource(ABC):
    """单路摄像头视频帧源抽象基类（由 Runtime ``TaskManager`` 托管生命周期）。"""

    @abstractmethod
    def open(self, stream_url: str) -> None:
        """打开视频流连接。"""

    @abstractmethod
    def read(self, stop_event: threading.Event) -> Frame | None:
        """读取最新一帧解码图像。

        Args:
            stop_event: 任务停止信号，实现方在重连退避期间应监听此事件以便及时退出。

        Returns:
            读取成功返回 :class:`Frame`；流暂时不可用/重连中或任务已停止时返回 ``None``。
        """

    def update_url(self, stream_url: str) -> None:
        """当 ``RenewLease`` 刷新鉴权 Token 后下发新流地址时由 Runtime 调用。"""

    @abstractmethod
    def close(self) -> None:
        """释放底层拉流解码器与句柄资源。"""


#: 根据任务上下文创建帧源的工厂函数类型。
FrameSourceFactory = Callable[[TaskContext], FrameSource]


class OpenCVStreamSource(FrameSource):
    """基于 OpenCV (``cv2.VideoCapture``) 的默认 RTSP 拉流基础服务实现。

    特性：
    - 延迟导入 ``cv2``，未安装 ``opencv-python`` 时不影响纯 ``BaseAlgorithm`` / 单元测试运行；
    - 强制 ``rtsp_transport;tcp``，避免高丢包环境下的花屏与解码崩溃；
    - 设置最小缓冲队列（``CAP_PROP_BUFFERSIZE = 1``），确保算法拿到最新低延迟帧；
    - 断流自动指数退避重连（1s → 2s → 4s → 最大 15s），并支持 ``update_url`` 热切流。
    """

    def __init__(
        self,
        *,
        reconnect_initial_sec: float = 1.0,
        reconnect_max_sec: float = 15.0,
        read_failure_threshold: int = 3,
    ) -> None:
        self._reconnect_initial = max(0.2, reconnect_initial_sec)
        self._reconnect_max = max(self._reconnect_initial, reconnect_max_sec)
        self._failure_threshold = max(1, read_failure_threshold)

        self._lock = threading.Lock()
        self._stream_url = ""
        self._pending_url: str | None = None
        self._cap = None
        self._frame_index = 0
        self._consecutive_failures = 0
        self._backoff = self._reconnect_initial
        self._closed = False

    def open(self, stream_url: str) -> None:
        with self._lock:
            self._stream_url = stream_url
            self._pending_url = None
            self._closed = False
        self._ensure_open()

    def update_url(self, stream_url: str) -> None:
        if not stream_url:
            return
        with self._lock:
            if stream_url != self._stream_url:
                self._pending_url = stream_url

    def read(self, stop_event: threading.Event) -> Frame | None:
        if self._closed or stop_event.is_set():
            return None

        # 检查是否有续租带来的新流地址，按需无缝重连
        with self._lock:
            pending = self._pending_url
            if pending is not None:
                self._stream_url = pending
                self._pending_url = None
        if pending is not None:
            logger.info("Stream URL refreshed; reopening capture source")
            self._release_cap()

        if not self._ensure_open():
            stop_event.wait(self._backoff)
            self._backoff = min(self._backoff * 2.0, self._reconnect_max)
            return None

        ok, img = self._cap.read()
        if not ok or img is None:
            self._consecutive_failures += 1
            if self._consecutive_failures >= self._failure_threshold:
                logger.warning(
                    "Stream read failed %d times consecutively; triggering reconnect",
                    self._consecutive_failures,
                )
                self._release_cap()
                stop_event.wait(self._backoff)
                self._backoff = min(self._backoff * 2.0, self._reconnect_max)
            return None

        self._consecutive_failures = 0
        self._backoff = self._reconnect_initial
        self._frame_index += 1
        height, width = int(img.shape[0]), int(img.shape[1])
        return Frame(
            image=img,
            width=width,
            height=height,
            timestamp_ms=int(time.time() * 1000),
            frame_index=self._frame_index,
            color_format="bgr",
        )

    def close(self) -> None:
        with self._lock:
            self._closed = True
        self._release_cap()

    def _ensure_open(self) -> bool:
        if self._cap is not None and self._cap.isOpened():
            return True
        if not self._stream_url:
            return False
        try:
            import cv2  # type: ignore[import-not-found]
        except ImportError as exc:
            raise RuntimeError(
                "FrameAlgorithm requires 'opencv-python-headless' (or a custom frame_source_factory) "
                "to decode RTSP streams."
            ) from exc

        # 优先使用 RTSP over TCP 低延迟配置
        os.environ.setdefault("OPENCV_FFMPEG_CAPTURE_OPTIONS", "rtsp_transport;tcp")
        cap = cv2.VideoCapture(self._stream_url, cv2.CAP_FFMPEG)
        if not cap.isOpened():
            cap.release()
            return False
        try:
            cap.set(cv2.CAP_PROP_BUFFERSIZE, 1)
        except Exception:
            pass
        self._cap = cap
        self._consecutive_failures = 0
        return True

    def _release_cap(self) -> None:
        cap = self._cap
        self._cap = None
        if cap is not None:
            try:
                cap.release()
            except Exception:
                pass
