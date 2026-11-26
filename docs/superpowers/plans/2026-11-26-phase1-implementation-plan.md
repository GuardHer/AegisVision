# 工业视频智能分析与联动报警 Web 平台 — 分阶段工程实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一套面向工业内网、低延迟、多进程可扩展的视频智能分析与联动报警 Web 平台（Go 5 平台进程 + 纯 Go `aegis-agent` + Python Runtime SDK/Mock + Vue Pure Admin 精简国际版前端 + Docker Compose/裸机双形态部署）。

**Architecture:** 摄像头 RTSP 经 MediaMTX 免转码 WebRTC (WHEP) 直通浏览器原始画面，同时向计算主机 Python Runtime 提供授权内部 RTSP（50~150ms 低延迟）；Go 平台拆分为 `aegis-api`、`aegis-scheduler`、`aegis-ingest`、`aegis-ws`、`aegis-dispatcher` 5 个独立进程，配合 PostgreSQL、Redis（热状态 + Pub/Sub 实时画框广播）、RabbitMQ（持久化报警队列与 DLX 重试、短视频录制任务、调度控制）与 S3/MinIO（图片与 MP4 证据存储）；计算主机运行纯 Go `aegis-agent` 主动 gRPC 连接平台、按 `runtime_key` 启停保活多个本地 Python Runtime 并管理租约与断网缓存续传；前端由 Nginx 独立托管并统一反代。

**Tech Stack:** Go 1.23+, Gin, `pgx/v5`, `sqlc`, `goose`, gRPC/Protobuf, `coder/websocket`, `amqp091-go`, `go-redis/v9`, AWS SDK Go v2 (S3/MinIO), MediaMTX, PostgreSQL 16, RabbitMQ 3.13+, Redis 7+, MinIO, Nginx, Vue Pure Admin 精简国际版 (`pure-admin-thin` `i18n`, Vue 3 + TS + Vite + Element Plus), Python 3.10+ (`grpcio`)。

**Spec:**
- `docs/superpowers/specs/2026-11-26-video-ai-alarm-platform-design.md`
- `docs/design/01-database-and-storage.md`
- `docs/design/02-protocols-and-apis.md`
- `docs/design/03-deployment-and-ops.md`
- `docs/design/04-go-coding-standards.md`

---

## Global Constraints

- 后端语言为 Go，纯 Go 无 CGO；不在 Go 进程内引入 OpenCV/FFmpeg/ONNX Runtime 绑定。
- 主数据库为 PostgreSQL（`pgx/v5` + `sqlc` + `goose`）；MQ 为 RabbitMQ；缓存与实时帧广播为 Redis；流媒体网关为 MediaMTX；对象存储为 S3 兼容存储（内网默认 MinIO，抽象 `ObjectStorage` 接口支持 `local` 兜底）。
- 平台下发给 Runtime 的授权拉流地址默认使用授权内部 RTSP（延迟目标 50~150ms），字段保持通用 `stream_url` 兼容可选 HLS。
- 报警为单次触发（不发恢复事件），首期支持三类区域规则：`anomaly_detect`、`count_threshold`、`absence_detect`。
- 多用户 RBAC 固定内置 `admin` / `operator` / `viewer` 三角色，覆盖 API、WS、MediaMTX 播放/拉流与证据访问。
- 前端使用 Vue Pure Admin 精简国际版（`pure-admin-thin` `i18n`），由 Nginx 独立托管与反代，**禁止 `go:embed` 内嵌进 Go 二进制**。
- 交付必须同时支持 Docker Compose（含离线内网镜像包流程）与裸机部署（Linux systemd / Windows 服务），无任何外网 CDN 依赖。

---

## Review Focus

以下 5 类高风险故障/边界输入必须由对应任务的自动化测试锁定，不允许遗漏：

1. **算法崩溃或摄像头断流时的离岗检测误报**：当 `absence_detect` 规则超过 `heartbeat_timeout_seconds`（默认 5s）未收到任何帧/空帧心跳时，离岗计时器必须暂停，不得触发虚假离岗报警（锁定于 Task 8）。
2. **Agent 断网重连重复上报导致重复报警**：`aegis-agent` 离线缓存通过 `ReportBufferedEvents` 重复发送同一 `event_id` 时，平台必须基于 `alarms.event_dedup_key` 幂等返回成功，不重复落库也不重复触发 Webhook（锁定于 Task 8 & Task 10）。
3. **Agent 崩溃后 Runtime 孤儿进程泄漏 GPU 显存**：当 Runtime 超过 `lease_ttl_seconds` 未收到 `RenewLease` 时，必须自动停止对应任务的拉流与推理并释放资源（锁定于 Task 11）。
4. **S3 证据链接越权访问与 MP4 拖拽播放**：未登录且无有效 HMAC 签名或签名过期的 `/api/v1/evidence/:alarm_id/image|video` 请求必须返回 `401`；合法签名请求必须支持 `Range: bytes=...` 并返回 `206 Partial Content`（锁定于 Task 4）。
5. **外部 Webhook 挂起与慢速浏览器 WS 客户端反压**：Webhook 接收端超时不可阻塞其他通道消费，需进入 RabbitMQ DLX 退避重试并记录 `alarm_deliveries`；慢速前端 WS 连接必须丢弃旧 `frame_detections` 保最新帧，不得阻塞其他在线客户端或导致内存泄漏（锁定于 Task 9 & Task 7）。

---

## Repository File Structure

```text
AegisVision/
├── docs/                                    # 设计、协议、Go 规范、部署与实施计划文档
├── deploy/
│   ├── nginx/nginx.conf                     # 统一入口与反代配置模板
│   ├── mediamtx/mediamtx.yml                # MediaMTX 低延迟与外部鉴权模板
│   ├── docker/docker-compose.yml            # 平台侧 11 服务离线可交付编排
│   ├── agent/docker-compose.yml             # 算力机侧 aegis-agent + Python Runtime 编排
│   └── baremetal/                           # Linux systemd units & Windows service 安装脚本
├── web/                                     # 基于 pure-admin-thin (i18n) 定制的 Web 管理后台（Nginx 独立托管）
│   └── src/components/aegis/               # 独立原生组件：WhepPlayer.vue、CanvasOverlay.vue、RoiEditor.vue
├── runtime/python-sdk/
│   ├── aegis_runtime_sdk/                  # Python Runtime gRPC 服务端基类、租约看门狗、心跳辅助
│   └── mock_runtime/                        # Mock Python Runtime（模拟多任务、异常、人数变化、离岗空帧）
└── backend/                                 # 单一 Go Module（纯 Go，无 CGO）
    ├── go.mod
    ├── Makefile                             # 统一收口 proto / sqlc / lint / test / build-all
    ├── .golangci.yml                        # 统一静态检查门禁配置
    ├── sqlc.yaml
    ├── api/proto/
    │   ├── buf.yaml & buf.gen.yaml
    │   ├── agent/v1/agent.proto             # aegis-agent <-> aegis-ingest 双向控制、实时帧与离线事件契约
    │   └── runtime/v1/runtime.proto         # aegis-agent <-> 本机 Python Runtime 任务、租约与结果契约
    ├── gen/go/                              # buf 生成的 pb / gRPC 代码（严禁手改）
    ├── migrations/                          # goose SQL 迁移文件
    ├── cmd/
    │   ├── aegis-api/main.go               # 控制面 REST API / RBAC / 流鉴权 / 签名证据代理 + migrate 子命令
    │   ├── aegis-scheduler/main.go         # 全局集群调度、版本任务归并、租约续期、MediaMTX 同步
    │   ├── aegis-ingest/main.go            # Agent 主动 gRPC 接入、命令透传、内存规则引擎
    │   ├── aegis-ws/main.go                # 浏览器 WebSocket 实时推流网关（Redis Pub/Sub）
    │   ├── aegis-dispatcher/main.go        # RabbitMQ 报警分发器（Notifier 插件）+ 异步短视频证据录制
    │   └── aegis-agent/main.go               # 计算主机纯 Go 节点代理（Runtime 启停、租约、分流缓存）
    └── internal/
        ├── domain/                          # 纯领域实体、值对象、常量与统一业务错误码（零框架依赖）
        ├── config/                          # 统一配置加载（YAML + AEGIS_ 环境变量 + Validate）
        ├── platform/
        │   ├── postgres/                    # pgx 连接池 + sqlc 查询层 + WithTx
        │   ├── storage/                     # ObjectStorage 接口 + S3(MinIO) / local 驱动
        │   ├── rabbitmq/                    # RabbitMQ 拓扑声明、发布、DLX 重试与竞争消费封装
        │   ├── redis/                       # Redis 封装（Token、选主锁、Agent 路由、Pub/Sub）
        │   ├── mediamtx/                    # MediaMTX Control API 客户端与授权流地址构造器
        │   ├── logger/                      # 标准库 log/slog 结构化 JSON 日志封装
        │   └── telemetry/                   # /healthz、/readyz 与 Prometheus /metrics
        ├── auth/                            # JWT、Argon2id/bcrypt、RBAC 中间件、HMAC 签名校验
        ├── api/                             # Gin 路由与各业务域 Handler/Service
        ├── scheduler/                       # 选主循环、任务去重归并、亲和打分、租约续期与掉线迁移
        ├── rules/                           # 纯内存区域规则引擎（射线法/相交、异常/人数/离岗、冷却防抖）
        ├── ingest/                          # Agent gRPC 服务实现、路由注册、结果处理与 S3 抓拍上传
        ├── ws/                              # WebSocket Hub、订阅管理、非阻塞丢旧保新广播
        ├── notifier/                        # Notifier 插件接口、Registry、Webhook 实现
        ├── evidence/                        # 异步短视频证据录制器（MediaMTX + ffmpeg CLI -> S3）
        └── agent/                           # aegis-agent 核心：平台连接、Runtime 进程管理、本机 gRPC 客户端、本地 SQLite/WAL 缓存
```

---

## Phase 1: 工程底座、数据模型、Protobuf 契约与基础设施封装

### Task 1: 初始化 Go 模块、工程门禁、配置加载与 PostgreSQL Schema + `sqlc` 数据层

**Files:**
- Create: `backend/go.mod`, `backend/Makefile`, `backend/.golangci.yml`, `backend/sqlc.yaml`
- Create: `backend/internal/domain/errors.go`, `backend/internal/config/config.go`, `backend/internal/platform/logger/logger.go`
- Create: `backend/migrations/00001_init_schema.sql`
- Create: `backend/internal/platform/postgres/queries/*.sql`（按业务域拆分：`users.sql`, `algorithms.sql`, `cameras.sql`, `rules.sql`, `tasks.sql`, `alarms.sql`）
- Test: `backend/internal/config/config_test.go`, `backend/internal/platform/postgres/store_test.go`

**Interfaces:**
- Produces:
  - `config.Load(path string) (*config.Config, error)`
  - `postgres.New(ctx context.Context, dsn string) (*postgres.Store, error)`（含 `WithTx`）
  - `postgres.RunMigrations(ctx context.Context, dsn string) error`
  - `sqlc` 生成的强类型查询接口 `postgres.Querier`（覆盖 `01-database-and-storage.md` 全部 13 张表及部分唯一索引 `uq_active_task_cam_ver`）

- [ ] **Step 1: 编写配置加载、Fail-Fast 校验与部分唯一索引约束失败测试**
  - `TestConfigLoadEnvOverride`：校验 YAML 默认值与环境变量覆盖（`AEGIS_POSTGRES_DSN`、`AEGIS_REDIS_ADDR`、`AEGIS_RABBITMQ_URL`、`AEGIS_S3_ENDPOINT`、`AEGIS_JWT_SECRET`、`AEGIS_EVIDENCE_SIGN_SECRET`）。
  - `TestActiveInferenceTaskUniqueness`：向 `inference_tasks` 插入两条相同 `(camera_id, algorithm_version_id)` 且 `desired_state='running'` 的记录，断言数据库返回唯一键冲突错误；一条 `stopped` 一条 `running` 允许通过。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/config/... ./internal/platform/postgres/... -v`
  - Expected: FAIL（包与迁移文件不存在）
- [ ] **Step 3: 实现工程底座、`internal/domain/errors.go`、`internal/config/config.go`、`migrations/00001_init_schema.sql` 与 `sqlc` 查询**
  - 严格遵循 `docs/design/04-go-coding-standards.md` 与 `01-database-and-storage.md`；执行 `make sqlc` 生成强类型 Go 访问层。
- [ ] **Step 4: 运行测试与 Lint 验证通过**
  - Run: `cd backend && make lint && go test -race ./internal/config/... ./internal/platform/postgres/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: initialize backend go module, lint gate, config, migrations, and sqlc store"`

---

### Task 2: 定义并经 `buf` 生成两套 gRPC Protobuf 契约（`agent.proto` & `runtime.proto`）

**Files:**
- Create: `backend/api/proto/buf.yaml`, `backend/api/proto/buf.gen.yaml`
- Create: `backend/api/proto/agent/v1/agent.proto`
- Create: `backend/api/proto/runtime/v1/runtime.proto`
- Create: `backend/gen/go/agent/v1/*.pb.go`, `backend/gen/go/runtime/v1/*.pb.go`
- Create: `runtime/python-sdk/aegis_runtime_sdk/pb/*_pb2*.py`
- Test: `backend/api/proto/proto_contract_test.go`

**Interfaces:**
- Produces:
  - `agentv1.AgentGatewayServer` / `agentv1.AgentGatewayClient`（`ControlStream`, `StreamLiveFrames`, `ReportBufferedEvents`）
  - `runtimev1.AlgorithmRuntimeServer` / `runtimev1.AlgorithmRuntimeClient`（`Health`, `AssignTask`, `UpdateTask`, `RevokeTask`, `RenewLease`, `StreamResults`）

- [ ] **Step 1: 编写 Protobuf 往返序列化与必需字段断言测试**
  - `TestAgentAndRuntimeProtoRoundTrip`：验证 `AssignTaskCommand` 携带 `stream_url`、`stream_protocol`、`lease_id`、`lease_ttl_seconds`、`rois`、`algo_params_json`，以及 `BufferedEvent` 携带 `event_id` 与原始 `snapshot_jpeg` bytes 无损序列化。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test ./api/proto/... -v`
  - Expected: FAIL
- [ ] **Step 3: 按 `docs/design/02-protocols-and-apis.md` 第 2、3 节编写 `.proto` 并通过 `make proto`（`buf lint` + `buf generate`）生成 Go 与 Python 代码**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./api/proto/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: add buf-managed agent and runtime gRPC protobuf contracts for go and python"`

---

### Task 3: 封装基础设施客户端（S3/Local `ObjectStorage`、RabbitMQ 拓扑、Redis 缓存与 Pub/Sub、MediaMTX Control API）

**Files:**
- Create: `backend/internal/platform/storage/storage.go`, `backend/internal/platform/storage/s3.go`, `backend/internal/platform/storage/local.go`
- Create: `backend/internal/platform/rabbitmq/rabbitmq.go`, `backend/internal/platform/rabbitmq/topology.go`
- Create: `backend/internal/platform/redis/redis.go`
- Create: `backend/internal/platform/mediamtx/client.go`
- Create: `backend/internal/platform/telemetry/health.go`
- Test: `backend/internal/platform/storage/storage_test.go`, `backend/internal/platform/rabbitmq/rabbitmq_test.go`, `backend/internal/platform/redis/redis_test.go`, `backend/internal/platform/mediamtx/client_test.go`

**Interfaces:**
- Produces:
  - `storage.ObjectStorage`（`PutObject`, `GetObject`, `DeleteObject`）及 `storage.NewS3(...)` / `storage.NewLocal(...)`
  - `rabbitmq.Client`（`EnsureTopology`, `PublishAlarm`, `PublishEvidenceRecord`, `PublishAgentCommand`, `PublishConfigEvent`, `Consume`）
  - `redis.Client`（`SetStreamToken`, `GetStreamToken`, `AcquireSchedulerLeader`, `RenewSchedulerLeader`, `SetAgentRoute`, `GetAgentRoute`, `PublishFrame`, `SubscribeFrames`, `PublishRealtimeAlarm`, `SubscribeRealtimeAlarms`）
  - `mediamtx.Client`（`UpsertPath`, `DeletePath`, `BuildAuthorizedRTSPURL`, `BuildWHEPPath`）

- [ ] **Step 1: 编写 4 个基础设施包的单元测试（使用 mock HTTP/miniredis/local driver + `integration` 构建标签）**
  - 覆盖：S3/Local Range 读取、`EnsureTopology` 声明 `aegis.alarms` + DLX 延迟阶梯（`3s/15s/60s`）+ `alarms.dlq`、`AcquireSchedulerLeader` 互斥抢锁、`UpsertPath` 按 `record_video` 开关生成短期录像参数。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/platform/storage/... ./internal/platform/rabbitmq/... ./internal/platform/redis/... ./internal/platform/mediamtx/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 4 个基础设施客户端包与探针组件**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/platform/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: add s3 object storage, rabbitmq topology, redis cache/pubsub, and mediamtx client"`

---

## Phase 2: 控制面服务 `aegis-api`（认证/RBAC、配置管理、流鉴权与 S3 证据代理）

### Task 4: 实现认证、RBAC 中间件、MediaMTX 外部鉴权与 HMAC 签名证据代理

**Files:**
- Create: `backend/internal/auth/password.go`, `backend/internal/auth/jwt.go`, `backend/internal/auth/signature.go`, `backend/internal/auth/middleware.go`
- Create: `backend/internal/api/handlers_auth.go`, `backend/internal/api/handlers_mediamtx_auth.go`, `backend/internal/api/handlers_evidence.go`
- Test: `backend/internal/auth/auth_test.go`, `backend/internal/api/handlers_evidence_test.go`, `backend/internal/api/handlers_mediamtx_auth_test.go`

**Interfaces:**
- Consumes: `postgres.Store`, `redis.Client`, `storage.ObjectStorage`
- Produces:
  - `auth.SignEvidenceURL(baseURL, alarmID, kind string, ttl time.Duration) string`
  - `auth.VerifyEvidenceSignature(alarmID, kind, exp, sig string, now time.Time) bool`
  - `auth.RequireRoles(roles ...string) gin.HandlerFunc`
  - HTTP Endpoints: `/api/v1/auth/*`, `/internal/mediamtx/auth`, `/api/v1/evidence/:alarm_id/image|video`

- [ ] **Step 1: 编写权限矩阵、流鉴权与 Review Focus #4 证据签名 Range 测试**
  - `TestRBACMiddleware`：验证 `viewer` 调用写接口返回 `403`，`operator` 可处理报警不可改摄像头，`admin` 全通。
  - `TestMediaMTXAuthCallback`：有效 `stream:play:<token>` 与 `stream:pull:<token>` 返回 `200`，伪造/过期/路径不匹配返回 `401`。
  - `TestEvidenceProxySecurityAndRange`（Review Focus #4）：无 Token 无签名返回 `401`；过期签名返回 `401`；篡改 `alarm_id` 返回 `401`；合法签名带 `Range: bytes=0-1023` 返回 `206 Partial Content` 且 `Content-Range` 正确。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/auth/... ./internal/api/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现认证模块、RBAC 中间件、MediaMTX 鉴权回调与流式证据代理 Handler**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/auth/... ./internal/api/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement jwt/rbac auth, mediamtx auth callback, and signed s3 evidence proxy"`

---

### Task 5: 实现 `aegis-api` 业务 CRUD 接口与进程入口

**Files:**
- Create: `backend/internal/api/router.go`
- Create: `backend/internal/api/handlers_users.go`, `backend/internal/api/handlers_algorithms.go`, `backend/internal/api/handlers_cameras.go`, `backend/internal/api/handlers_rules.go`, `backend/internal/api/handlers_nodes_tasks.go`, `backend/internal/api/handlers_alarms.go`, `backend/internal/api/handlers_channels.go`
- Create: `backend/cmd/aegis-api/main.go`（含 `migrate up` 与默认初始管理员引导逻辑）
- Test: `backend/internal/api/handlers_crud_test.go`

**Interfaces:**
- Consumes: `postgres.Store`, `redis.Client`, `rabbitmq.Client`, `storage.ObjectStorage`, `auth.*`
- Produces: `docs/design/02-protocols-and-apis.md` 第 4 节全部 REST API，并在摄像头/ROI/规则/算法版本写操作成功后发布 RabbitMQ `config.events`；在手动重发投递时重新发布至 `aegis.alarms`

- [ ] **Step 1: 编写核心 CRUD、配置变更事件发布与报警处理审计测试**
  - `TestCameraROIRuleCRUDAndConfigEvent`：验证创建摄像头、归一化坐标校验（拒绝超出 `[0,1]` 的 ROI 顶点）、绑定多算法版本规则，并断言 RabbitMQ 收到 `config.events`。
  - `TestAlarmHandleAuditAndManualRetry`：验证 `operator` 标记 `confirmed` 记录 `handled_by/handled_at`，点击手动重发重新入队。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/api/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现全部业务 Handler 与 `backend/cmd/aegis-api/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/api/... ./cmd/aegis-api/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement aegis-api rest endpoints and binary entrypoint"`

---

## Phase 3: 全局调度大脑 `aegis-scheduler`

### Task 6: 实现选主、任务合并去重、亲和调度、租约续期、节点掉线迁移与 MediaMTX 同步

**Files:**
- Create: `backend/internal/scheduler/planner.go`, `backend/internal/scheduler/reconciler.go`, `backend/internal/scheduler/lease.go`
- Create: `backend/cmd/aegis-scheduler/main.go`
- Test: `backend/internal/scheduler/planner_test.go`, `backend/internal/scheduler/reconciler_test.go`

**Interfaces:**
- Consumes: `postgres.Store`, `redis.Client`, `rabbitmq.Client`, `mediamtx.Client`
- Produces:
  - `scheduler.Planner.ComputeDesiredTasks(rules, nodes) []DesiredTask`
  - `scheduler.Reconciler.Run(ctx context.Context) error`（Redis 选主后运行定时对账、配置事件响应、租约每 `TTL/3` 续约并刷新 MediaMTX 授权 RTSP Token、经 RabbitMQ `agent.cmd.<ingest_id>` 定向下发指令）

- [ ] **Step 1: 编写任务去重、亲和打分、掉线迁移与选主测试**
  - `TestPlannerDeduplicatesCameraAlgorithmVersion`：同一摄像头两个 ROI 引用同一 `algorithm_version_id`，断言只生成 1 条 `DesiredTask` 且 `rois` 包含两个区域。
  - `TestPlannerAffinityAndPinnedNode`：验证优先调度到已运行该摄像头其他任务的同节点，且 `pinned` 规则强制绑定目标节点。
  - `TestReconcilerFailoverOnNodeTimeout`：节点超过 15s 无心跳时自动迁移其任务到其他具备该 `runtime_key` 的健康节点，并撤销旧任务。
  - `TestSchedulerLeaderElectionStandbyTakeover`：双实例竞争 Redis 锁，主节点释放/过期后备节点接管对账循环。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/scheduler/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 `planner.go`、`reconciler.go`、`lease.go` 与 `backend/cmd/aegis-scheduler/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/scheduler/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement aegis-scheduler with leader election, dedup, leases, and failover"`

---

## Phase 4: 实时推送网关 `aegis-ws` 与核心接入规则引擎 `aegis-ingest`

### Task 7: 实现 `aegis-ws` 浏览器 WebSocket 实时推送网关

**Files:**
- Create: `backend/internal/ws/hub.go`, `backend/internal/ws/server.go`
- Create: `backend/cmd/aegis-ws/main.go`
- Test: `backend/internal/ws/hub_test.go`

**Interfaces:**
- Consumes: `redis.Client`（Redis Pub/Sub `frames:<camera_id>`, `alarms:realtime`）, `auth.JWTVerifier`
- Produces: `ws.Server`（按 `camera_ids` 订阅隔离推送；每个客户端独立有界发送缓冲，满时丢弃旧 `frame_detections` 保留最新帧，绝不丢 `alarm_alert`）

- [ ] **Step 1: 编写订阅过滤与 Review Focus #5 慢客户端背压隔离测试**
  - `TestWSHubCameraSubscriptionIsolation`：客户端 A 订阅 cam1、客户端 B 订阅 cam2，验证互不串流且均收到全局告警。
  - `TestWSHubSlowClientDropsOldFramesWithoutBlockingFastClient`（Review Focus #5）：阻塞客户端 A 的写入通道并高频推送 100 帧，断言客户端 B 仍即时收到最新帧且客户端 A 缓冲不无限增长。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/ws/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 `backend/internal/ws/` 与 `backend/cmd/aegis-ws/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/ws/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement aegis-ws realtime websocket gateway with drop-oldest backpressure"`

---

### Task 8: 实现区域规则引擎与 `aegis-ingest`（Agent gRPC 接入 + 规则评估 + S3 抓拍）

**Files:**
- Create: `backend/internal/rules/geometry.go`, `backend/internal/rules/engine.go`
- Create: `backend/internal/ingest/gateway.go`, `backend/internal/ingest/processor.go`
- Create: `backend/cmd/aegis-ingest/main.go`
- Test: `backend/internal/rules/geometry_test.go`, `backend/internal/rules/engine_test.go`, `backend/internal/ingest/gateway_test.go`

**Interfaces:**
- Consumes: `agentv1.AgentGatewayServer`, `postgres.Store`, `redis.Client`, `rabbitmq.Client`, `storage.ObjectStorage`
- Produces:
  - `rules.Engine.Evaluate(frame DetectionFrame) EvalOutput`（返回 `ROICounts`、`TriggeredAlarms`）
  - `ingest.Gateway`（处理 `ControlStream` / `StreamLiveFrames` / `ReportBufferedEvents`，维护 Redis `agent:route:<node_id>` 并消费 RabbitMQ `agent.cmd.<ingest_id>` 透传命令给在线 Agent）

- [ ] **Step 1: 编写几何判定、三类规则、Review Focus #1 离岗停表与 Review Focus #2 断网重放幂等测试**
  - `TestGeometryHitModes`：测试 `bottom_center`、`center`、`bbox_intersect` 与 `full_frame`。
  - `TestRuleEngineAnomalyCountAndCooldown`：验证连续帧命中、`count > max_count` 超员报警与冷却期内不重复报警。
  - `TestRuleEngineAbsencePausesOnHeartbeatTimeout`（Review Focus #1）：离岗规则 `duration=10s, heartbeat_timeout=5s`，在空帧心跳持续到来 10s 时触发离岗报警；若第 4s 起停止心跳，推进时钟 30s **不得触发任何离岗报警**。
  - `TestIngestBufferedEventIdempotency`（Review Focus #2）：通过 `ReportBufferedEvents` 两次上报同一 `event_id` 异常事件，断言数据库仅存在 1 条 `alarms` 记录且 RabbitMQ 仅发布 1 次报警消息，两次均返回 `EventAck`。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/rules/... ./internal/ingest/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 `backend/internal/rules/`、`backend/internal/ingest/` 与 `backend/cmd/aegis-ingest/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/rules/... ./internal/ingest/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement roi rule engine and aegis-ingest grpc gateway"`

---

## Phase 5: 联动报警分发与异步短视频证据录制 `aegis-dispatcher`

### Task 9: 实现 `Notifier` 插件化框架、Webhook 分发器与异步短视频证据录制器

**Files:**
- Create: `backend/internal/notifier/notifier.go`, `backend/internal/notifier/registry.go`, `backend/internal/notifier/webhook.go`
- Create: `backend/internal/evidence/recorder.go`
- Create: `backend/cmd/aegis-dispatcher/main.go`
- Test: `backend/internal/notifier/webhook_test.go`, `backend/internal/evidence/recorder_test.go`

**Interfaces:**
- Consumes: `postgres.Store`, `rabbitmq.Client`, `storage.ObjectStorage`, `auth.SignEvidenceURL`
- Produces:
  - `notifier.Notifier` 接口（`Type()`, `ValidateConfig()`, `Send()`）与 `notifier.Registry`
  - `notifier.WebhookNotifier`（实现 `docs/design/02-protocols-and-apis.md` 第 7 节负载与 HMAC-SHA256 签名）
  - `evidence.Recorder.RecordAndUpload(ctx, req) (objectKey string, err error)`（异步拉取/切片 MP4 上传 S3 并回填 `alarms.video_object_key` 与 `video_status`）

- [ ] **Step 1: 编写 Webhook 签名、Review Focus #5 超时重试隔离与视频证据回填测试**
  - `TestWebhookNotifierPayloadAndHMACSignature`：断言请求头 `X-Aegis-Signature` 与签名公式完全一致且携带签名证据 URL。
  - `TestWebhookTimeoutRoutesToDLXWithoutBlockingOtherChannel`（Review Focus #5）：模拟通道 A 挂起超时、通道 B 正常，断言通道 B 快速成功落日志，通道 A 按超时中断并写入 `failed` 触发 DLX 退避重试。
  - `TestEvidenceRecorderUpdatesAlarmVideoKey`：使用模拟切片输出，断言上传 S3 后 `alarms.video_status` 更新为 `ready` 且 `video_object_key` 非空；失败时标记 `failed` 不影响原报警。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/notifier/... ./internal/evidence/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 `backend/internal/notifier/`、`backend/internal/evidence/` 与 `backend/cmd/aegis-dispatcher/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/notifier/... ./internal/evidence/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement aegis-dispatcher notifier plugins, webhook retry, and async video evidence recorder"`

---

## Phase 6: 纯 Go `aegis-agent` 与 Python Runtime SDK / Mock Runtime

### Task 10: 实现纯 Go `aegis-agent`（主动连平台、按 Runtime 键启停进程、租约续期与分流本地缓存）

**Files:**
- Create: `backend/internal/agent/runtime_manager.go`, `backend/internal/agent/local_buffer.go`, `backend/internal/agent/agent.go`
- Create: `backend/cmd/aegis-agent/main.go`
- Test: `backend/internal/agent/runtime_manager_test.go`, `backend/internal/agent/local_buffer_test.go`, `backend/internal/agent/agent_test.go`

**Interfaces:**
- Consumes: `agentv1.AgentGatewayClient`, `runtimev1.AlgorithmRuntimeClient`
- Produces:
  - `agent.RuntimeManager`（按 `runtime_key` 懒启动本机 Runtime 进程、健康探测、多任务复用同 Runtime、崩溃退避重启、0 任务空闲自动回收）
  - `agent.LocalBuffer`（基于纯 Go 嵌入式 SQLite/WAL 落盘缓存异常事件与抓拍 `bytes`，重连后按 `event_id` 顺序补报并在收到 `EventAck` 后清理）
  - `agent.NodeAgent`（分流策略：实时画框帧内存直通不落盘、异常帧断网落盘续传）

- [ ] **Step 1: 编写 Runtime 多任务复用、空闲回收、分流缓存与断网续传测试**
  - `TestRuntimeManagerLazyStartAndMultiTaskReuse`：同 `runtime_key` 下发 2 个任务，断言只启动 1 个 Runtime 进程；全部撤销并超过空闲超时后进程优雅退出。
  - `TestAgentSplitForwardingLiveDropVsAbnormalDiskBuffer`（关联 Review Focus #2）：模拟平台断连期间收到 50 帧普通检测帧与 2 帧异常抓拍帧，断言仅 2 帧异常事件落盘本地缓存；恢复连接后 2 帧成功补报并清盘，50 帧普通历史框不补发。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend && go test -race ./internal/agent/... -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 `backend/internal/agent/` 与 `backend/cmd/aegis-agent/main.go`**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `cd backend && go test -race ./internal/agent/... -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement pure-go aegis-agent with runtime lifecycle, lease renewal, and split local buffering"`

---

### Task 11: 实现 Python Runtime gRPC SDK 模板与 Mock Python Runtime（含租约看门狗）

**Files:**
- Create: `runtime/python-sdk/pyproject.toml`
- Create: `runtime/python-sdk/aegis_runtime_sdk/server.py`, `runtime/python-sdk/aegis_runtime_sdk/base.py`, `runtime/python-sdk/aegis_runtime_sdk/lease.py`
- Create: `runtime/python-sdk/mock_runtime/main.py`
- Test: `runtime/python-sdk/tests/test_runtime_lease_and_tasks.py`

**Interfaces:**
- Consumes: `runtime.proto` 生成的 Python stub
- Produces:
  - `BaseAlgorithm` 抽象基类（算法工程师只需实现 `load()`、`infer_task(task_ctx)`）
  - `RuntimeServer`（完整实现 `AlgorithmRuntime` gRPC 服务、单进程多任务、每 1s 空帧心跳、租约过期自动停任务）
  - `mock_runtime`（可通过参数模拟异常命中、人数变化与无人离岗，便于全链路集成联调）

- [ ] **Step 1: 编写多任务并发与 Review Focus #3 租约到期自停止测试**
  - `test_lease_expiry_auto_stops_task`（Review Focus #3）：分配 `lease_ttl_seconds=1` 的任务且不调用 `RenewLease`，等待 1.5s 后断言任务已从活跃表清除且回调 `stop()` 释放资源。
  - `test_runtime_emits_empty_heartbeat_frames`：断言无检测目标时仍按周期输出 `is_heartbeat=True, detections=[]`。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `pytest runtime/python-sdk/tests -v`
  - Expected: FAIL
- [ ] **Step 3: 实现 Python Runtime SDK 与 Mock Runtime**
- [ ] **Step 4: 运行测试验证通过**
  - Run: `pytest runtime/python-sdk/tests -v`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: add python runtime gRPC sdk with lease watchdog and mock runtime"`

---

## Phase 7: Web 前端（Vue Pure Admin 精简国际版 + 原生低延迟视频与画框组件）

### Task 12: 初始化 `pure-admin-thin`（`i18n`）并实现独立原生视频/画框/ROI 组件

**Files:**
- Create: `web/`（基于 `pure-admin-thin` `i18n` 分支清理冗余示例并对接 `aegis-api` JWT/Refresh/RBAC 动态菜单与 `zh-CN`/`en` 词条）
- Create: `web/src/components/aegis/WhepPlayer.vue`
- Create: `web/src/components/aegis/CanvasOverlay.vue`
- Create: `web/src/components/aegis/RoiEditor.vue`
- Create: `web/src/utils/videoRect.ts`（精确计算 `object-fit: contain` 实际渲染视频矩形与归一化坐标映射）
- Test: `web/src/utils/__tests__/videoRect.spec.ts`, `web/src/components/aegis/__tests__/CanvasOverlay.spec.ts`

**Interfaces:**
- Consumes: `/api/v1/cameras/:id/stream-token`、`/whep/<stream_path>/whep`、`/ws/realtime`
- Produces:
  - `WhepPlayer.vue`（原生 `RTCPeerConnection` WHEP 客户端、断流自动退避重连、主/子码流平滑切换）
  - `CanvasOverlay.vue`（原生 Canvas 2D 60fps 渲染检测框、标签、置信度、ROI 轮廓与区域人数；1.5s 超时自动清空陈旧画框）
  - `RoiEditor.vue`（独立 SVG 叠加层多边形 ROI 鼠标绘制、顶点拖拽、闭合与删除，输出归一化 `[0,1]` 顶点）

- [ ] **Step 1: 编写坐标映射、黑边偏移与 1.5s 陈旧画框清理单元测试**
  - `videoRect.spec.ts`：测试 16:9 视频在 4:3 与 21:9 容器中归一化点 `(0,0)`、`(1,1)` 映射回像素坐标的精确值及逆映射。
  - `CanvasOverlay.spec.ts`：推送检测框后推进假时钟 1600ms，断言画布被清空。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `pnpm --dir web test:unit`
  - Expected: FAIL
- [ ] **Step 3: 实现 `videoRect.ts` 及 3 个独立原生视觉组件，完成 `pure-admin-thin` (`i18n`) 登录与 RBAC 路由对接**
- [ ] **Step 4: 运行测试与生产构建验证通过**
  - Run: `pnpm --dir web test:unit && pnpm --dir web build`
  - Expected: PASS，生成纯离线无外部 CDN 的 `web/dist/`
- [ ] **Step 5: Commit**
  - `git commit -m "feat: initialize pure-admin-thin i18n web frontend with native whep player, canvas overlay, and roi editor"`

---

### Task 13: 实现平台全部业务页面与中英文国际化

**Files:**
- Create: `web/src/views/monitor/index.vue`（1/4/9 宫格实时监控大屏 + 侧边实时报警流 + 叠加开关）
- Create: `web/src/views/algorithms/index.vue`（算法目录与版本管理）
- Create: `web/src/views/cameras/index.vue` & `web/src/views/cameras/config.vue`（摄像头 CRUD + ROI 编辑 + 按区域配置多算法规则与录像开关）
- Create: `web/src/views/nodes/index.vue`（算力主机 `aegis-agent`、Runtime 状态与任务租约监控）
- Create: `web/src/views/alarms/index.vue`（报警中心分页筛选、抓拍图叠加还原、S3 MP4 在线播放、投递日志与手动重发、确认/误报审计）
- Create: `web/src/views/channels/index.vue` & `web/src/views/users/index.vue`（报警通道管理/测试、用户与角色管理）
- Create: `web/locales/zh-CN.yaml`, `web/locales/en.yaml`
- Test: `web/src/views/__tests__/permissions.spec.ts`

- [ ] **Step 1: 编写前端角色菜单/按钮权限可见性测试**
  - `permissions.spec.ts`：断言 `viewer` 隐藏配置菜单与报警处理按钮，`operator` 仅显示监控与报警处理，`admin` 全量可见。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `pnpm --dir web test:unit`
  - Expected: FAIL
- [ ] **Step 3: 实现全部业务视图页面与中英文词条**
- [ ] **Step 4: 运行测试、类型检查与生产构建验证通过**
  - Run: `pnpm --dir web typecheck && pnpm --dir web test:unit && pnpm --dir web build`
  - Expected: PASS
- [ ] **Step 5: Commit**
  - `git commit -m "feat: implement monitoring, algorithm versions, roi rules, nodes, alarms, and admin views"`

---

## Phase 8: 双形态部署打包（Docker Compose + 裸机）与全链路端到端验证

### Task 14: 编写 Nginx / MediaMTX / Docker Compose / 裸机服务配置并完成全链路端到端验收

**Files:**
- Create: `deploy/nginx/nginx.conf`
- Create: `deploy/mediamtx/mediamtx.yml`
- Create: `deploy/docker/docker-compose.yml`, `deploy/docker/Dockerfile.go`, `deploy/docker/Dockerfile.nginx`, `deploy/docker/export-offline.sh`
- Create: `deploy/agent/docker-compose.yml`, `deploy/agent/agent.yaml.example`
- Create: `deploy/baremetal/systemd/*.service`, `deploy/baremetal/windows/install-services.ps1`, `deploy/baremetal/README.md`
- Create: `backend/tests/e2e/e2e_pipeline_test.go`

- [ ] **Step 1: 编写端到端验收测试（模拟 RTSP 源 → MediaMTX → `aegis-agent` + Mock Python Runtime → `aegis-ingest` → `aegis-ws` + S3(MinIO) + RabbitMQ → `aegis-dispatcher` → Mock Webhook）**
  - 覆盖 4 条完整端到端断言：① 登录 + 申请 WebRTC 流 Token + 拉流成功；② 异常命中后 1 秒内收到 WS 实时框、报警记录落库、S3 图片可读、Mock Webhook 收到合法 HMAC 签名请求；③ 离岗空帧心跳持续触发离岗报警且断流停表不误报；④ 开启视频证据后异步生成 S3 MP4 且签名 Range 请求返回 `206`。
- [ ] **Step 2: 编写全部 Docker Compose、Nginx、MediaMTX 与裸机部署文件**
- [ ] **Step 3: 启动本地全栈 Docker Compose 运行端到端验证**
  - Run: `docker compose -f deploy/docker/docker-compose.yml up -d --build && cd backend && go test -tags=e2e ./tests/e2e/... -v`
  - Expected: 全部 11 平台服务 + `aegis-agent`/Mock Runtime 健康启动，E2E 测试全绿通过
- [ ] **Step 4: 验证裸机交叉编译构建脚本**
  - Run: `cd backend && make build-all`
  - Expected: 6 个二进制（`aegis-api`、`aegis-scheduler`、`aegis-ingest`、`aegis-ws`、`aegis-dispatcher`、`aegis-agent`）零错误产出
- [ ] **Step 5: Commit**
  - `git commit -m "chore: add docker compose, baremetal deployment manifests, and e2e verification"`
