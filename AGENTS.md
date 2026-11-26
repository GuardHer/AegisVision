# AGENTS.md — AegisVision 工程协作与 AI 代理规范

本文件为所有参与 **AegisVision**（`github.com/GuardHer/AegisVision`）开发的工程师与 AI 编码代理提供硬性约束与工作指南。在编写或修改任何代码前，请务必阅读本文件及对应的专题设计文档。

---

## 1. 核心架构红线（不可违背）

1. **纯 Go 无 CGO**：禁止在 Go 代码中引入 CGO、OpenCV、FFmpeg C 绑定或 ONNX Runtime C 绑定。`aegis-agent` 不碰视频解码；仅平台侧在需要异步 MP4 切片、黑盒单帧抓拍兜底或 H.265 可选转码时通过受控外部 `ffmpeg` CLI 执行。
2. **前后端严格分离，禁止 `go:embed` 前端**：前端工程位于 `web/`（基于 Vue Pure Admin 精简国际版 `pure-admin-thin` `i18n`），构建产物 `web/dist/` 由 **Nginx** 独立托管并反代 `/api/`、`/ws/`、`/whep/`，严禁将前端静态文件内嵌进 Go 二进制。
3. **低延迟流协议原则**：Web 预览使用 MediaMTX WebRTC (WHEP) 原生硬解 + 前端 Canvas 2D 叠加，禁止服务端烧录画框重编码；下发给 Runtime 的 `stream_url` **默认必须为授权内部 RTSP**（低延迟 50~150ms），不可默认改为高延迟 HLS。
4. **实时流与可靠队列严禁混用**：
   - 高频瞬态实时检测框仅走 `aegis-agent` 内存直发 → `aegis-ingest` → **Redis Pub/Sub** → `aegis-ws`（有界队列、慢客户端丢旧保新），**严禁写入 RabbitMQ 持久化队列或 Agent 本地磁盘缓存**；
   - 异常事件、抓拍存证与外部联动通知走 Agent 本地 SQLite/WAL 断网缓存（`event_id` 幂等去重）→ PostgreSQL + S3 → **RabbitMQ 持久化队列（含 DLX 退避重试）**。
5. **视频证据绝不阻塞报警**：触发报警后必须立即存图片、落库、推前端实时告警与发 Webhook；短视频 MP4 证据一律通过 RabbitMQ `evidence.record` 由 `aegis-dispatcher` 异步录制上传 S3 并回填。
6. **离岗检测防误报**：`absence_detect` 仅在任务在线且最近 `heartbeat_timeout_seconds`（默认 5s）内收到有效帧或空帧心跳时推进计时，断流/节点离线立即暂停计时。
7. **离线内网零外部依赖**：前端构建产物与运行时严禁依赖任何公网 CDN 字体、脚本或图标。

---

## 2. 权威文档索引（改代码前先查对应真源）

| 涉及变更域 | 必读权威文档 |
| :--- | :--- |
| 架构与核心流程 | [`DESIGN.md`](./DESIGN.md)、[`docs/superpowers/specs/2026-11-26-video-ai-alarm-platform-design.md`](./docs/superpowers/specs/2026-11-26-video-ai-alarm-platform-design.md) |
| 表结构、索引、JSONB、S3 存储 | [`docs/design/01-database-and-storage.md`](./docs/design/01-database-and-storage.md) |
| gRPC `.proto`、REST API、RBAC、WS、MQ、Redis、Webhook | [`docs/design/02-protocols-and-apis.md`](./docs/design/02-protocols-and-apis.md) |
| Go 目录结构、包隔离、日志、错误、并发与 Lint | [`docs/design/04-go-coding-standards.md`](./docs/design/04-go-coding-standards.md) |
| Nginx、MediaMTX、Docker Compose、裸机与扩容 | [`docs/design/03-deployment-and-ops.md`](./docs/design/03-deployment-and-ops.md) |
| 分阶段任务与验收测试 | [`docs/superpowers/plans/2026-11-26-phase1-implementation-plan.md`](./docs/superpowers/plans/2026-11-26-phase1-implementation-plan.md) |

---

## 3. 各子目录工程约束

### 3.1 `backend/`（Go 单一 Module：`github.com/GuardHer/AegisVision/backend`）
- **6 个进程入口**：`cmd/aegis-api`、`cmd/aegis-scheduler`、`cmd/aegis-ingest`、`cmd/aegis-ws`、`cmd/aegis-dispatcher`、`cmd/aegis-agent`。
- **包隔离规则**：
  - `cmd/*` 仅做配置加载、显式构造函数 DI 组装与优雅停机，严禁写业务逻辑；
  - `internal/domain` 零第三方框架依赖；
  - 6 个进程业务包（`api`、`scheduler`、`ingest`、`ws`、`notifier`/`evidence`、`agent`）**彼此禁止直接 `import`**（仅允许 `ingest` 调用纯逻辑包 `rules`）。
- **数据库与协议**：禁止 `AutoMigrate`，全部 DDL 走 `migrations/`（`goose`），查询全部走 `sqlc` 生成，多表写走 `WithTx`；全部 `.proto` 使用 **`buf`** 管理（`buf lint` + `buf breaking` + `buf generate`），禁止手改 `gen/go/`。
- **日志与错误**：统一用标准库 **`log/slog`**（JSON 结构化日志，禁 `fmt.Println`），禁止日志泄露密码/Token/Secret；对外 HTTP 响应使用 `domain.AppError`，禁止泄露原生 SQL 或堆栈；禁止无理由吞错。
- **并发与停机**：全链路透传带超时的 `context.Context`；禁止野生 Goroutine（必须由 `errgroup` 或 `Run(ctx)` 托管）；规则引擎状态按 `camera_id` 分片锁避免全局大锁；全进程实现 `SIGINT/SIGTERM` 10s 内优雅停机。

### 3.2 `web/`（Vue Pure Admin 精简国际版）
- 基于 `pure-admin-thin`（`i18n` 分支），所有新增界面文案必须同步维护 `web/locales/zh-CN.yaml` 与 `web/locales/en.yaml`。
- 核心视觉组件（`web/src/components/aegis/WhepPlayer.vue`、`CanvasOverlay.vue`、`RoiEditor.vue`）必须与中后台模板解耦，基于浏览器原生 `RTCPeerConnection`、Canvas 2D 与 SVG 实现；坐标严格采用 `[0.0, 1.0]` 归一化并按 `object-fit: contain` 实际渲染视频矩形换算。

### 3.3 `runtime/`（Python Runtime SDK & Mock）
- 严格遵守 `backend/api/proto/runtime/v1/runtime.proto`；必须内置租约看门狗（超过 `lease_ttl_seconds` 未收到 `RenewLease` 自动停止拉流与推理释放显存）及无目标空帧心跳（默认每 1s）。

---

## 4. 开发流程与提交前门禁

- **遵循 TDD**：先写失败测试，验证失败后再写最小实现使测试通过。
- **后端验证命令**（在 `backend/` 下）：
  - 改 `.proto` 后：`make proto`（含 `buf lint` & `buf breaking`）
  - 改 SQL 后：`make sqlc`
  - 提交前必跑：`make lint && make test`（`go test -race ./...`）
- **前端验证命令**：
  - 提交前必跑：`pnpm --dir web typecheck && pnpm --dir web test:unit && pnpm --dir web build`
- **Python SDK 验证命令**：
  - 提交前必跑：`pytest runtime/python-sdk/tests -v`
- **架构变更同步**：任何新增表、变更协议、增减进程或修改中间件拓扑的 PR，必须同步更新 `DESIGN.md` 与 `docs/design/` 中对应文档。
