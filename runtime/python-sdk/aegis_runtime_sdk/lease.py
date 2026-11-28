"""向后兼容导出模块：请优先从 ``aegis_runtime_sdk.runtime`` 或顶层 ``aegis_runtime_sdk`` 导入。"""

from aegis_runtime_sdk.runtime.lease import LeaseWatchdog

__all__ = ["LeaseWatchdog"]
