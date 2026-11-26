# AegisVision — 系统架构与设计总纲

本文档是 **AegisVision** 的顶层工程设计总纲，汇总核心架构决策、进程拓扑、关键链路与扩展机制；数据库 DDL、完整 Protobuf/API 协议、Go 编码规范及部署配置详见 `docs/design/` 下的专题文档。

---

## 1. 核心设计原则

1. **流媒体与推理旁路解耦（方案 A）**：摄像头 RTSP 由 MediaMTX 统一收口，浏览器经 WebRTC (WHEP) 免转码拉取原始低延迟画面（200~500ms），算法节点经授权内部 RTSP（50~150ms）直连拉流，前端通过透明 Canvas 2D 实时叠加检测框与 ROI，避免服务端烧录画框重编码带来的高延迟与双流带宽浪费。
2. **平台工程与算法模型彻底解耦**：平台负责版本管理、任务调度、租约管理、规则判定与报警联动；每台算力机运行 1 个纯 Go `aegis-agent` 负责出站连平台、按 `runtime_key` 启停本机 Runtime 与断网续传；算法工程师只需用 Python 实现约定的 `runtime.proto` gRPC 协议，单 Runtime 可复用多任务，是否共享拉流解码由 Runtime 自主决定。
3. **实时链路与可靠链路严格分流**：
   - 高频瞬态检测框（用于前端实时画框）：`aegis-agent` 内存直发（不落盘）→ `aegis-ingest` → **Redis Pub/Sub** → `aegis-ws` → 浏览器（慢客户端丢旧保新），保证亚毫秒转发与零堆积；
   - 异常事件、抓拍存证与联动报警：`aegis-agent` 断网本地 SQLite/WAL 落盘续传（带 `event_id` 幂等去重）→ `aegis-ingest` 同步上传图片至 **S3/MinIO** 并落库 PostgreSQL → **RabbitMQ** 持久化队列 + DLX 指数退避重试 → `aegis-dispatcher` 异步投递外部 Webhook 并异步录制短视频 MP4 证据。
4. **前后端分离、多进程可扩展**：前端 `web/` 由 **Nginx** 独立托管与统一反代，禁止内嵌进 Go 二进制；Go 后端 5 个平台进程均支持多实例或主备高可用。

---

## 2. 进程拓扑与职责划分

### 2.1 Go 平台后端（5 个独立进程，同一 `backend/` Go Module）
| 进程名 | 扩展与高可用模式 | 核心职责 |
| :--- | :--- | :--- |
| **`aegis-api`** | 多实例无状态（Nginx 负载均衡） | REST API（用户/RBAC、算法与版本管理、摄像头/ROI/规则、节点任务查询、报警中心与审计）；MediaMTX 外部鉴权回调（校验前端 WebRTC 播放 Token 与 Runtime 授权拉流 Token）；HMAC 签名 S3 证据代理（支持 MP4 `Range` 拖拽播放）；配置变更广播至 RabbitMQ。 |
| **`aegis-scheduler`** | 主备高可用（Redis 分布式锁选主） | 全局调度大脑：按 `(camera_id, algorithm_version)` 合并去重期望任务；按 `runtime_key` 匹配、摄像头亲和与负载均衡分配算力机（支持可选手工指定节点）；维护任务租约（Lease）续期与授权流 Token 刷新；同步 MediaMTX 路径；节点心跳超时自动故障迁移；通过 RabbitMQ 定向路由指令至持有目标 Agent 长连接的 `aegis-ingest`。 |
| **`aegis-ingest`** | 多实例水平扩展（L4 均衡） | 承接 `aegis-agent` 主动发起的 gRPC 双向长连接（在 Redis 登记 `agent:route:<node_id>` 并透传调度指令）；持续接收实时检测帧与断网补报事件；运行内存态区域规则引擎（`anomaly_detect`、`count_threshold`、`absence_detect` 与规则粒度冷却防抖）；实时框推 Redis Pub/Sub，报警同步存 S3 图片、落 PostgreSQL 并投递 RabbitMQ。 |
| **`aegis-ws`** | 多实例无状态（Nginx 负载均衡） | 浏览器 WebSocket 实时推流网关（JWT 鉴权），订阅 Redis Pub/Sub 按 `camera_id` 隔离推送实时检测框、区域人数与实时告警弹窗（慢连接丢旧保新）。 |
| **`aegis-dispatcher`** | 多实例竞争消费（RabbitMQ 均摊） | ① 消费 RabbitMQ `alarms.triggered` 队列，经插件化 `Notifier` 接口推送 Webhook（含 HMAC 签名与签名证据 URL），借助 DLX 实现 `3s -> 15s -> 60s -> dlq` 退避重试并写 `alarm_deliveries`；<br>② 消费 RabbitMQ `evidence.record` 队列，对开启视频证据的规则异步从 MediaMTX 截取报警前后 MP4 上传 S3 并回填 `alarms.video_object_key`，绝不阻塞实时报警。 |

### 2.2 计算主机侧（每台算力机 1 个纯 Go Agent + 0~N 个 Runtime）
- **`aegis-agent`**：纯 Go 无 CGO/无 `ffmpeg`，主动出站 gRPC 连接 `aegis-ingest`；按 `runtime_key` 懒启动、保活并在空闲时回收本机多个 Runtime 进程；透传授权 RTSP 地址、ROI、参数与租约并定时续约；接收 Runtime 结果后分流上报（实时框内存直发、异常事件本地落盘断点续传）。
- **Python Runtime**：算法工程师使用 PyTorch / ONNX Runtime / TensorRT 实现 `runtime.proto` gRPC 协议；单进程支持多任务并发；租约到期未收到续约自动停任务释放显存；画面无目标时持续发送空帧心跳保障离岗检测准确计时。

### 2.3 基础设施组件（6 个常驻 + 平台侧按需 `ffmpeg`）
`nginx`、`mediamtx`、`postgres`、`rabbitmq`、`redis`、`minio`（S3 兼容存储，内网默认）。

---

## 3. 关键业务与技术机制

1. **按区域配置与去重调度**：配置层为 `摄像头 -> ROI 区域（含全画面） -> 绑定多个算法版本规则`；调度层自动合并同摄像头同算法版本任务，减少重复拉流解码。
2. **离岗检测防误报**：仅在任务在线且最近 `heartbeat_timeout_seconds`（默认 5s）内收到帧/空帧心跳时推进离岗计时，摄像头断流或 Runtime 崩溃立即停表不误报。
3. **S3 证据与统一入口访问**：数据库仅存相对 `object_key`，通过 S3 Lifecycle 自动按天数清理过期图片（默认 90d）与视频（默认 30d）；所有外部与浏览器访问统一经 `Nginx -> aegis-api` 校验 JWT 或 HMAC 临时签名，不暴露 MinIO 端口。
4. **全链路 RBAC**：固定内置 `admin`、`operator`、`viewer` 三角色；覆盖 API、WS、MediaMTX 播放/拉流与证据下载。
5. **双形态离线部署**：同时提供 Docker Compose（含离线镜像导出/导入脚本）与裸机（Nginx + 6 个 Go 二进制 + `systemd`/Windows 服务）交付方案。

---

## 4. 专题设计文档索引

| 文档 | 覆盖内容 |
| :--- | :--- |
| [`docs/superpowers/specs/2026-11-26-video-ai-alarm-platform-design.md`](./docs/superpowers/specs/2026-11-26-video-ai-alarm-platform-design.md) | 完整需求背景、非目标、反目标与三方案对比选型记录 |
| [`docs/design/01-database-and-storage.md`](./docs/design/01-database-and-storage.md) | PostgreSQL 13 张表 DDL、索引、JSONB Schema 与 S3/MinIO 规范 |
| [`docs/design/02-protocols-and-apis.md`](./docs/design/02-protocols-and-apis.md) | `agent.proto`、`runtime.proto`、REST API、RBAC 矩阵、WS、RabbitMQ、Redis、Webhook 规范 |
| [`docs/design/03-deployment-and-ops.md`](./docs/design/03-deployment-and-ops.md) | 端口规划、Nginx/MediaMTX 配置模板、Docker 离线交付、裸机与扩容运维手册 |
| [`docs/design/04-go-coding-standards.md`](./docs/design/04-go-coding-standards.md) | `backend/` 单模块目录结构、包隔离铁律、`log/slog`、`buf`、并发与 `.golangci.yml` 门禁 |
| [`docs/superpowers/plans/2026-11-26-phase1-implementation-plan.md`](./docs/superpowers/plans/2026-11-26-phase1-implementation-plan.md) | 8 阶段 14 个 TDD 任务的详细实施计划与高风险审查重点 |
