# AegisVision Python Algorithm Runtime SDK

本 SDK 用于将 Python 视觉 AI 算法（PyTorch / ONNX / TensorRT / YOLO / DeepStream 等）快速接入 AegisVision 平台。

---

## 一、架构分层：Runtime 基础服务 vs 算法实现内容

SDK 在包结构上严格划分为两层，确保算法工程师只需关注模型推理本身：

```text
aegis_runtime_sdk/
├── contract/          # 【算法接入契约层】给算法工程师阅读与实现
│   ├── models.py      #   Point / ROI / Detection / Frame / FrameOutput / TaskContext / AlgorithmMetadata
│   └── algorithm.py   #   FrameAlgorithm（推荐）/ BaseAlgorithm（自定义拉流）
├── runtime/           # 【Runtime 基础服务层】平台全托管，算法无需实现
│   ├── config.py      #   RuntimeConfig（环境变量与运行参数）
│   ├── lease.py       #   LeaseWatchdog（租约看门狗防显存泄漏）
│   ├── stream.py      #   FrameSource / OpenCVStreamSource（RTSP TCP 拉流、断流重连、Token 刷新切流）
│   ├── result_bus.py  #   ResultBus（有界队列、慢客户端丢旧保新、坐标裁剪、Protobuf 序列化）
│   ├── task_manager.py#   TaskManager（多路任务调度、target_fps 控频、空帧心跳、异常自动抓拍）
│   └── server.py      #   RuntimeServer / serve()（gRPC 协议适配与优雅停机）
└── pb/                # gRPC 自动生成代码（对应 backend/api/proto/runtime/v1/runtime.proto）
```

### 职责边界对照表

| 能力模块 | 归属层级 | 说明 |
| :--- | :--- | :--- |
| gRPC `AlgorithmRuntime` 服务端与健康探针 | **Runtime 基础服务** | 自动响应 `Health / AssignTask / UpdateTask / RevokeTask / RenewLease / StreamResults` |
| 租约看门狗（防显存泄漏） | **Runtime 基础服务** | 超过 `lease_ttl_seconds` 未续租自动停止任务并回调 `on_task_stop` |
| 多路任务并发调度与异常隔离 | **Runtime 基础服务** | 每路摄像头独立工作线程，单路异常不波及其他点位，支持 `max_tasks` 保护 |
| RTSP 拉流、断流退避重连、Token 续期切流 | **Runtime 基础服务** | `FrameAlgorithm` 模式下自动托管，断流期间暂停心跳防止离岗误报 |
| 动态推理帧率控制（`target_fps`） | **Runtime 基础服务** | 自动读取 `algo_params["target_fps"]` 节流 |
| 无目标空帧心跳（`is_heartbeat=True`） | **Runtime 基础服务** | 有效画面无目标时定时发送心跳，保障 `absence_detect` 离岗计时精准 |
| 异常帧自动 JPEG 抓拍编码 | **Runtime 基础服务** | 检测到 `is_abnormal=True` 且未手填 `snapshot_jpeg` 时自动编码 |
| 有界队列分发与坐标合法性裁剪 | **Runtime 基础服务** | 慢客户端丢旧保新，自动将 `bbox` 与 `confidence` 裁剪至 `[0.0, 1.0]` |
| 加载模型权重与资源释放（`load` / `unload`） | **算法实现** | 进程启动加载模型一次，退出释放显存 |
| 单帧前向推理与标签输出（`infer_frame`） | **算法实现** | 输入 `Frame` + `TaskContext`，输出 `list[Detection]` |
| 每路任务跟踪器/状态管理（`on_task_*`） | **算法实现（可选）** | 按 `task_ctx.task_id` 隔离各路摄像头 Tracker 状态 |

---

## 二、两种接入模式对比

1. **`FrameAlgorithm`（推荐，适用 95% 的算法）**
   - 只需实现 `load()` 和 `infer_frame(frame, task_ctx)`；
   - 拉流、重连、Token 切换、控频、空帧心跳、异常自动抓拍全部由 Runtime 托管；
   - 完整示范见 [`examples/yolo_style_algorithm.py`](./examples/yolo_style_algorithm.py)。
2. **`BaseAlgorithm`（高级模式，自定义拉流/硬件解码管线）**
   - 适用于算法内部自带解码管线（如 DeepStream / GStreamer / C++ 推理服务代理）或纯 Mock 测试；
   - 实现 `load()` 和 `infer_task(task_ctx)`，注意：**有效空画面请返回 `FrameOutput(detections=[])`**（触发心跳），**断流/未就绪请返回 `None`**（暂停心跳防误报）。

---

## 三、快速接入示例（15 行代码）

```python
from aegis_runtime_sdk.contract import Detection, Frame, FrameAlgorithm, TaskContext
from aegis_runtime_sdk.runtime import serve


class MyHelmetAlgorithm(FrameAlgorithm):
    def load(self) -> None:
        self.model = load_my_model("helmet.onnx")

    def infer_frame(self, frame: Frame, task_ctx: TaskContext) -> list[Detection]:
        boxes = self.model.predict(frame.image, conf=task_ctx.confidence_threshold)
        return [
            Detection.from_xyxy_pixels(
                x1, y1, x2, y2, frame.width, frame.height,
                label=label,
                confidence=score,
                is_abnormal=(label == "no_helmet"),
            )
            for x1, y1, x2, y2, label, score in boxes
        ]


if __name__ == "__main__":
    serve(MyHelmetAlgorithm(), runtime_key="py-yolo-helmet-v1")
```

---

## 四、给算法工程师的关键契约要点

- **坐标一律归一化至 `[0.0, 1.0]`**：原点在画面左上角，建议直接使用 `Detection.from_xyxy_pixels(...)` / `Detection.from_xywh_pixels(...)` 自动换算。
- **ROI 辅助方法**：`ROI.to_pixel_points(w, h)` 可直接转像素多边形，`ROI.contains_point(x, y)` 可做归一化落点判定。
- **线程安全**：同一 Runtime 进程会并发跑多路摄像头，请按 `task_ctx.task_id` 隔离跟踪器与历史缓存，并在 `on_task_stop` 中及时清理。
- **安装与测试**：
  ```bash
  pip install -e ".[vision,dev]"
  pytest tests -v
  ```
