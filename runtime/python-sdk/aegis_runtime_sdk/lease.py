"""Lease watchdog guaranteeing tasks auto-stop if aegis-agent crashes or stops renewing."""

from __future__ import annotations

import threading
import time
from typing import Callable


class LeaseWatchdog:
    """Tracks per-task lease deadlines and triggers on_expire when TTL elapses without renewal."""

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
        self._thread = threading.Thread(target=self._loop, name="aegis-lease-watchdog", daemon=True)
        self._thread.start()

    def register(self, task_id: str, lease_id: str, ttl_seconds: int) -> None:
        ttl = max(1, int(ttl_seconds))
        deadline = time.monotonic() + ttl
        with self._lock:
            self._leases[task_id] = (lease_id, deadline)

    def renew(self, task_id: str, lease_id: str, ttl_seconds: int) -> bool:
        ttl = max(1, int(ttl_seconds))
        deadline = time.monotonic() + ttl
        with self._lock:
            if task_id not in self._leases:
                return False
            self._leases[task_id] = (lease_id, deadline)
            return True

    def unregister(self, task_id: str) -> None:
        with self._lock:
            self._leases.pop(task_id, None)

    def stop(self) -> None:
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
                    pass
