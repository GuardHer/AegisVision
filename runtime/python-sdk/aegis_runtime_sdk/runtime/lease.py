"""Runtime 基础服务：任务租约看门狗（LeaseWatchdog）。

职责说明（属于平台基础服务，算法无需关心）：
- 维护每个活跃任务的租约截止时间（基于单调时钟 ``time.monotonic``，不受系统校时影响）；
- 当 ``aegis-agent`` 崩溃或网络隔离导致超过 ``lease_ttl_seconds`` 未收到 ``RenewLease`` 时，
  自动触发 ``on_expire(task_id)`` 停止任务并回调 ``on_task_stop``，彻底防止僵尸任务长期占用 GPU 显存。
"""

from __future__ import annotations

import logging
import threading
import time
from typing import Callable

logger = logging.getLogger("aegis.runtime.lease")


class LeaseWatchdog:
    """单调时钟驱动的任务租约看门狗。"""

    def __init__(
        self,
        on_expire: Callable[[str], None],
        check_interval_sec: float = 0.25,
    ) -> None:
        self._on_expire = on_expire
        self._check_interval = max(0.02, check_interval_sec)
        self._lock = threading.Lock()
        self._leases: dict[str, tuple[str, float]] = {}  # task_id -> (lease_id, monotonic_deadline)
        self._stop_event = threading.Event()
        self._thread = threading.Thread(
            target=self._loop,
            name="aegis-lease-watchdog",
            daemon=True,
        )
        self._thread.start()

    def register(self, task_id: str, lease_id: str, ttl_seconds: int) -> None:
        """注册新任务租约。"""
        ttl = max(1, int(ttl_seconds))
        deadline = time.monotonic() + ttl
        with self._lock:
            self._leases[task_id] = (lease_id, deadline)

    def renew(self, task_id: str, lease_id: str, ttl_seconds: int) -> bool:
        """续期活跃任务租约；若任务已不存在或已过期则返回 ``False``。"""
        ttl = max(1, int(ttl_seconds))
        deadline = time.monotonic() + ttl
        with self._lock:
            if task_id not in self._leases:
                return False
            self._leases[task_id] = (lease_id, deadline)
            return True

    def unregister(self, task_id: str) -> None:
        """任务正常撤销或停止时注销租约。"""
        with self._lock:
            self._leases.pop(task_id, None)

    def active_count(self) -> int:
        """返回当前受看门狗托管的活跃租约数。"""
        with self._lock:
            return len(self._leases)

    def stop(self) -> None:
        """停止看门狗后台扫描线程。"""
        self._stop_event.set()
        if self._thread.is_alive():
            self._thread.join(timeout=2.0)

    def _loop(self) -> None:
        while not self._stop_event.wait(self._check_interval):
            now = time.monotonic()
            expired: list[str] = []
            with self._lock:
                for task_id, (_, deadline) in list(self._leases.items()):
                    if now >= deadline:
                        expired.append(task_id)
                        del self._leases[task_id]
            for task_id in expired:
                try:
                    self._on_expire(task_id)
                except Exception:
                    logger.exception("on_expire callback failed for task %s", task_id)
