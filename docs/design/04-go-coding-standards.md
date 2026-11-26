# Go 工程规范与仓库目录结构指南

本文档锁定本项目 Go 后端与 `aegis-agent` 的代码组织、分层边界、编码约定、并发与生命周期铁律以及静态检查门禁，所有平台进程与算力机 Agent 均须严格遵循。

---

## 1. 仓库顶层布局与 Go 单模块结构

整个仓库采用**前后端与算法运行时清晰分目录**结构，Go 侧使用**单一 `go.mod`（位于 `backend/` 下）**统一管理全部 6 个进程，确保 `.proto` 与 `sqlc` 变更时全仓编译期联动校验、避免多模块版本错位。

```text
<project-root>/
├── docs/                              # 设计、协议、规范、部署与实施计划文档
├── deploy/                            # Docker Compose、Nginx、MediaMTX 与裸机服务配置
├── web/                               # Vue Pure Admin 精简国际版（Nginx 独立托管，不内嵌 Go）
├── runtime/                           # Python Runtime gRPC SDK 模板与 Mock Runtime
└── backend/                           # 单一 Go Module（纯 Go，无 CGO）
    ├── go.mod
    ├── go.sum
    ├── Makefile                       # 统一收口 proto / sqlc / lint / test / build-all
    ├── .golangci.yml                  # 统一静态检查门禁配置
    ├── sqlc.yaml                      # sqlc 代码生成配置
    ├── api/proto/                     # Protobuf 源文件与 buf.yaml / buf.gen.yaml
    │   ├── agent/v1/agent.proto       # aegis-agent <-> aegis-ingest 契约
    │   └── runtime/v1/runtime.proto   # aegis-agent <-> 本机 Python Runtime 契约
    ├── gen/go/                        # buf 生成的 pb / gRPC 代码（严禁手改）
    ├── migrations/                    # goose SQL 迁移脚本（只追加，严禁篡改已发布版本）
    ├── cmd/                           # 极薄进程入口（只做配置加载、显式组装与优雅停机）
    │   ├── aegis-api/main.go
    │   ├── aegis-scheduler/main.go
    │   ├── aegis-ingest/main.go
    │   ├── aegis-ws/main.go
    │   ├── aegis-dispatcher/main.go
    │   └── aegis-agent/main.go
    └── internal/
        ├── domain/                    # 纯领域实体、值对象、常量与统一业务错误码（零框架依赖）
        ├── config/                    # YAML + 环境变量加载与 Validate() 快速失败校验
        ├── platform/                  # 基础设施公共底座
        │   ├── postgres/              # pgxpool、sqlc 生成层与 WithTx 事务封装
        │   ├── redis/                 # Token、选主锁、Agent 路由、Pub/Sub 实时广播
        │   ├── rabbitmq/              # 交换机/队列/DLX 声明、发布与竞争消费封装
        │   ├── storage/               # ObjectStorage 接口（S3/MinIO 主实现 + local 兜底）
        │   ├── mediamtx/              # MediaMTX Control API 客户端与授权流 URL 构造
        │   ├── logger/                # 标准库 log/slog 结构化 JSON 日志封装
        │   └── telemetry/             # /healthz、/readyz 探针与 Prometheus /metrics
        ├── auth/                      # JWT、Argon2id/bcrypt、RBAC 中间件、HMAC 证据签名
        ├── api/                       # aegis-api 专属：Gin 路由、中间件与按业务域拆分的 Handler/Service
        ├── scheduler/                 # aegis-scheduler 专属：选主、任务归并、亲和打分、租约续期与迁移
        ├── rules/                     # 纯内存区域规则引擎（几何判定、异常/人数/离岗、冷却，零 IO）
        ├── ingest/                    # aegis-ingest 专属：Agent gRPC Server、指令透传、调 rules & S3
        ├── ws/                        # aegis-ws 专属：WebSocket Hub、订阅隔离、丢旧保新广播
        ├── notifier/                  # aegis-dispatcher 专属：Notifier 插件接口、Registry 与 Webhook
        ├── evidence/                  # aegis-dispatcher 专属：异步短视频证据切片与 S3 上传
        └── agent/                     # aegis-agent 专属：主动连平台、Runtime 生命周期、租约与本地缓存
```

---

## 2. 包依赖与分层铁律

1. **`cmd/*` 极薄原则**：每个 `main.go` 仅负责解析命令行/配置、调用构造函数显式组装依赖（Manual DI）、启动服务与响应停机信号，**禁止在 `cmd/` 中编写业务逻辑**。
2. **`internal/domain` 零框架依赖**：禁止 import Gin、`pgx`、`gRPC`、`amqp091-go`、`go-redis` 或 AWS SDK，确保领域语义纯粹且杜绝循环依赖。
3. **进程业务包严格隔离**：6 个进程对应的业务包（`api`、`scheduler`、`ingest`、`ws`、`notifier`/`evidence`、`agent`）**禁止彼此直接 `import`**；跨进程交互必须通过 `gen/go/` Protobuf、RabbitMQ、Redis 或 PostgreSQL 进行（仅允许 `ingest` 调用纯逻辑包 `rules`）。
4. **接口定义在使用方（Accept interfaces, return structs）**：
   - 只在存在多实现（如 `ObjectStorage`、`Notifier`）或需要单测解耦处由调用方定义最小接口；
   - **禁止机械式地为每个结构体配一对 `IXxxService` / `IXxxRepository` 空壳接口**。
5. **不引入反射 DI 容器**：坚持显式 `NewXxx(...)` 构造函数注入，调用链透明、编译期暴露依赖缺失、单测构造零魔法。

---

## 3. 错误处理与结构化日志规范

### 3.1 统一错误体系
- 在 `internal/domain/errors.go` 定义统一业务错误结构体 `AppError`：
  - `Code`：稳定整型/字符串业务错误码（前后端国际化文案映射依据）；
  - `HTTPStatus` / `GRPCCode`：对外协议状态码；
  - `Message`：对外安全展示消息（**严禁把原始 SQL、文件路径、内部地址或栈信息返回给前端**）；
  - `Err`：包装的底层原始错误（仅记录内部日志）。
- 向上传递错误统一使用 `fmt.Errorf("context action: %w", err)`，判断错误类型统一用 `errors.Is` / `errors.As`。
- **禁止吞错**：禁止无理由 `_ = fn()`；仅在明确无害的资源释放（如 `defer resp.Body.Close()`）允许忽略并按需记 debug 日志。

### 3.2 结构化日志（`log/slog`）
- 统一采用 Go 官方标准库 **`log/slog`**（生产默认 JSON Handler，开发可选 Text Handler），**严禁在业务代码中使用 `fmt.Println` / `log.Printf`**。
- 启动时在根 Logger 注入固定字段 `service`（如 `aegis-ingest`）与 `instance_id`；请求/任务/报警链路强制携带上下文字段：`request_id`、`node_id`、`camera_id`、`task_id`、`rule_id`、`alarm_id`。
- 禁止在日志中明文打印密码、JWT、RTSP URL 账号密码、Webhook Secret 或 S3 AccessKey/SecretKey（提供统一脱敏 helper）。

---

## 4. 并发、背压与优雅停机铁律

1. **`context.Context` 贯穿全链路**：所有数据库查询、Redis、RabbitMQ、S3、gRPC、HTTP、Webhook 与外部 `ffmpeg` 子进程调用必须传入 `ctx` 并设置合理超时；禁止在业务函数内部新建无超时的 `context.Background()`。
2. **严禁野生 Goroutine**：所有后台常驻循环与异步协程必须纳入 `errgroup.Group` 或带 `ctx.Done()` 退出的组件 `Run(ctx)` 生命周期管理，确保退出时无协程泄漏。
3. **规则引擎无全局大锁**：`internal/rules` 维护摄像头/规则内存状态时，按 `camera_id` 使用分片锁或单摄像头串行 Worker，避免多路高频帧互相阻塞；所有并发代码必须通过 `go test -race ./...`。
4. **实时流背压隔离**：
   - `aegis-ingest` → Redis Pub/Sub → `aegis-ws` → 浏览器发送队列，全程使用**有界通道 + 丢旧保新（drop-oldest）**策略；
   - `alarm_alert` 告警推送独立优先级，不得被高频画框淹没或丢弃。
5. **全进程统一优雅停机（Graceful Shutdown）**：
   - 全部 6 个进程监听 `SIGINT` / `SIGTERM`，在 10 秒宽限期内按序退出：
     1. 停止接收新 HTTP/WS/gRPC 流量（健康探针 `/readyz` 先切为不就绪）；
     2. `aegis-scheduler` 主动释放 Redis 选主锁以便备节点毫秒级接管；`aegis-dispatcher` 停止拉取新 RabbitMQ 消息并等待在途投递完成 ACK；
     3. `aegis-agent` 刷盘未上报的本地缓存并优雅通知本机 Runtime 进程释放资源；
     4. 按逆序关闭 RabbitMQ、Redis、PostgreSQL 连接池。

---

## 5. 数据库、Protobuf 与配置规范

- **PostgreSQL & `sqlc`**：
  - 所有表结构变更严格通过 `backend/migrations/`（`goose`）管理，禁止运行时 `AutoMigrate`；
  - 所有 SQL 查询集中放在 `internal/platform/postgres/queries/` 并经 `sqlc generate` 生成强类型 Go 代码，严禁字符串拼接 SQL；
  - 跨表一致性写入统一使用 `postgres.Store.WithTx(ctx, func(q *Queries) error)`。
- **Protobuf & `buf`**：
  - 使用 **`buf`** 管理 `backend/api/proto/`：提交前执行 `buf lint` 校验风格、`buf breaking` 防止破坏性协议变更影响现场已部署的 `aegis-agent` 与 Python Runtime，`buf generate` 同步生成 Go 与 Python 代码。
- **配置与快速失败**：
  - 统一通过 `config.yaml` + 环境变量覆盖（前缀 `AEGIS_`）加载，启动第一时间调用 `cfg.Validate()`，缺少关键配置或非法参数立即报错退出（Fail-Fast）。

---

## 6. 代码质量门禁、测试规范与统一命令

### 6.1 静态检查（`.golangci.yml`）
强制开启且 CI 必过：`govet`、`errcheck`、`staticcheck`、`revive`、`gosec`、`bodyclose`、`sqlclosecheck`、`errorlint`、`exhaustive`、`misspell`。

### 6.2 测试分层要求
- **单元测试（默认无外部依赖，强制开启竞态检测 `go test -race ./...`）**：
  - 覆盖核心纯逻辑与边界：`rules` 几何/异常/人数/离岗停表/冷却、`scheduler` 任务合并去重与亲和打分、`auth` RBAC 与 HMAC 签名、`ws` 慢客户端背压、`agent` 分流缓存与断点续传。
- **集成测试（`//go:build integration`）**：对接真实 PostgreSQL / Redis / RabbitMQ / MinIO 验证迁移、部分唯一索引、RabbitMQ DLX 重试与 S3 Range 代理。
- **端到端测试（`//go:build e2e`）**：完整串联 MediaMTX + Go 5 进程 + `aegis-agent` + Mock Python Runtime + Mock Webhook。

### 6.3 统一 `backend/Makefile` 入口
| 命令 | 用途 |
| :--- | :--- |
| `make proto` | 执行 `buf lint`、`buf breaking` 并重新生成 Go 与 Python Protobuf 代码 |
| `make sqlc` | 执行 `sqlc generate` 更新 PostgreSQL 强类型查询代码 |
| `make lint` | 运行 `golangci-lint run ./...` 静态门禁检查 |
| `make test` | 运行全部单元测试（含 `-race` 竞态检测） |
| `make test-integration` | 运行数据库/MQ/Redis/S3 集成测试 |
| `make build-all` | 交叉编译输出全部 6 个 Go 可执行文件至 `backend/bin/` |
