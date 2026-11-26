# 工业视频智能分析与联动报警 Web 平台 — 总体设计文档

- **日期**：2026-11-26
- **状态**：已定稿（待按实施计划落地）
- **规模判定**：独立完整项目（全栈 Web + MediaMTX 流媒体集成 + Go 5 进程可扩展平台后端 + Go `aegis-agent` 与 Python Runtime 体系 + RabbitMQ/Redis/PostgreSQL/S3(MinIO) + RBAC + Docker/裸机双形态部署）

---

## 1. 背景与核心意图（Intent & Why）

构建一套以**工业场景为主、底层逻辑通用**的视频智能分析与联动报警 Web 平台。

核心诉求是打造一个**低延迟、高可靠、多进程可扩展、软硬与算法彻底解耦**的工程底座：
- **平台侧（我们负责）**：统一管理**算法版本**、摄像头与 ROI 检测配置、多算力节点任务调度与租约管理、区域规则引擎（异常识别、人数统计、离岗检测）、多用户 RBAC 权限、S3 图片/视频证据存证、Web 报警中心与可扩展多通道联动报警。
- **计算节点侧（Go `aegis-agent` 由我们负责）**：每台算力主机运行 1 个纯 Go 编写的节点 Agent，主动连接平台、上报节点与 Runtime 状态、按 **Runtime 键**启停保活本机多个算法 Runtime 进程、透传授权流地址/ROI/算法参数/租约，并接收 Runtime gRPC 结果后做分流上报（实时框内存直发 + 异常事件本地落盘续传）。
- **算法推理侧（Runtime 由算法工程师负责）**：使用 Python（接入 PyTorch、ONNX Runtime、TensorRT 等任意框架）实现约定的 **Runtime gRPC 协议**即可接入；同一个 Runtime 进程可同时处理多个任务，视频拉流和解码是否共享由 Runtime 内部自主优化。

---

## 2. 目标、非目标与反目标

### 2.1 目标（Goals）
1. **极低延迟的实时预览与推理链路**：
   - 本地内网接入普通 RTSP 摄像头流，Web 端采用浏览器原生 `RTCPeerConnection` (WHEP) 免转码播放，局域网预览延迟目标 **200~500ms**。
   - 平台下发给 Runtime 的授权拉流地址**默认采用带短期鉴权 Token 的内部 RTSP 地址**（延迟仅 50~150ms；协议字段保持通用 `stream_url`，仅在特殊纯 HTTP 受限环境可选降级 HLS）。
2. **原始画面与检测结果分离叠加**：Web 端直接展示原始监控画面，实时监控页通过原生 **HTML5 Canvas 2D** 叠加绘制检测框、类别、置信度、区域实时人数及 ROI 轮廓（一键开关叠加层）；配置页通过独立原生组件（SVG / Konva）支持流畅的 ROI 多边形交互编辑。
3. **算法版本管理与多节点、多 Runtime 集群调度**：
   - 平台统一维护算法及版本（`algorithm_key + version`、`runtime_key`、启动规格、默认参数 Schema），支持版本切换与灰度更新。
   - 支持为摄像头划定多个检测区域（含全画面），按“**区域 × 算法版本**”绑定规则；调度器自动按 `(camera_id, algorithm_version)` 合并去重推理任务，基于节点负载与 `runtime_key` 亲和调度、维护任务**租约（Lease）**防僵尸进程，节点掉线自动迁移，同时支持可选手工指定节点。
4. **三类核心区域规则与防抖**：
   - **异常命中（`anomaly_detect`）**、**人数统计（`count_threshold`）**、**离岗检测（`absence_detect`）**；
   - 离岗检测结合 Runtime/Agent 帧心跳计时，杜绝断流或崩溃误报；规则粒度冷却防抖，避免刷屏。
5. **单次触发式可扩展联动报警与 S3 证据存储**：
   - 异常发生时触发一次报警，基于 **RabbitMQ** 持久化队列与 `Notifier` 插件接口异步可靠分发；首期内置 **Web 报警中心（含人工确认/误报审计）** 与 **Webhook 推送（DLX 自动退避重试 + 死信兜底 + 手动重发）**，预留 MQTT、Modbus、声光、短信等扩展点。
   - 异常证据（**触发抓拍图片 JPEG** + 可选**报警前后短视频证据 MP4**）统一存入 **S3 兼容对象存储（内网默认 MinIO）**，解决多实例共享访问与生命周期自动过期清理。
6. **多用户与角色权限控制（RBAC）**：覆盖 Web API、WebSocket、MediaMTX 前端 WebRTC 播放与 Runtime 拉流鉴权，以及签名证据图片/视频访问。
7. **多进程可扩展与双形态离线部署**：Go 平台拆分为 **5 个职责清晰的独立进程**（均支持多实例/主备高可用），前端由 **Nginx 独立托管**不内嵌 Go，同时支持 **Docker Compose** 与 **裸机** 离线内网部署。

### 2.2 非目标（Non-Goals）
1. **不自研或训练真实 AI 算法模型**（提供标准 Python Runtime SDK 模板与 Mock Runtime 用于开发与全链路验证）。
2. **首期不做“异常恢复/解除”状态事件**（仅在异常发生时触发一次报警）。
3. **首期不做公网穿透 / 多租户 SaaS / 国标 GB28181 平台级联**（聚焦本地内网标准 RTSP）。
4. **首期不做 7×24 小时全量 NVR 录像回放平台**（视频存储聚焦于**报警事件关联的短视频证据切片**）。
5. **不在 Go 进程内通过 CGO 集成 OpenCV/FFmpeg/ONNX Runtime**。

### 2.3 反目标（Anti-Goals）
1. **高延迟反模式**：给 Runtime 默认走高延迟 HLS 切片导致报警慢半拍；或把每秒上千帧的实时画框塞进 RabbitMQ 持久化队列或 Agent 磁盘缓存，导致前端延迟堆积或断网恢复后狂闪历史旧框。
2. **视频录制阻塞报警触达**：等待录完“报警后 10 秒短视频”才推送前端告警或发送 Webhook（视频证据必须异步录制补挂）。
3. **僵尸推理任务与单机磁盘耦合**：Agent 宕机后 Runtime 孤儿进程继续空占显存；或多实例部署时抓拍图存在单个 Go 容器本地盘导致其他实例读不到图。

---

## 3. 约束条件与锁定技术路线（Constraints & Tech Stack）

| 层级 | 选定技术栈 | 来源与选型说明 |
| :--- | :--- | :--- |
| **流媒体网关** | **MediaMTX** | 用户确认。RTSP 统一拉流中继、WebRTC (WHEP) 零转码出流、授权内部 RTSP（给 Runtime 低延迟拉流）、可选短期环形录像/切片源、Control API 与外部 HTTP 鉴权回调。 |
| **对象存储（证据）** | **S3 兼容存储（内网默认 MinIO）** | 用户确认。统一存储异常抓拍图片（JPEG）与短视频证据（MP4），天然支持 Go 多实例无共享盘访问，并利用 S3 Lifecycle 按天数自动清理过期证据；代码层抽象 `ObjectStorage` 接口（兼容任意 S3/OSS 及极简裸机本地目录兜底）。 |
| **消息队列（MQ）** | **RabbitMQ** | 用户确认。承担三类可靠队列：① 报警联动分发队列（`alarms.triggered` + DLX 延迟重试）；② 异步短视频证据录制队列（`evidence.record`）；③ 调度指令路由（`agent.cmd.<ingest_id>`）与配置变更广播（`config.events`）。 |
| **缓存与实时广播** | **Redis** | 用户确认。① **Redis Pub/Sub** 承担高频瞬态实时检测框与实时告警的亚毫秒级跨进程广播；② 存储热状态（MediaMTX 短期播放/拉流 Token、JWT 状态、Agent 在线路由映射与节点心跳负载、调度器选主分布式锁）。 |
| **主数据库** | **PostgreSQL** | 用户确认。唯一持久化真源（用户/RBAC、算法与版本、摄像头/ROI/规则、节点与任务/租约、报警记录与证据 `object_key`、投递日志）。 |
| **Go 后端与 Agent 栈** | **Go + Gin + `pgx/v5` + `sqlc` + `goose` + gRPC (Protobuf) + `coder/websocket` + AWS SDK v2 (S3)** | Go 为用户指定；数据层与框架为我方定稿。服务间、Agent↔平台、Runtime↔Agent 统一采用 **gRPC (Protobuf)**；浏览器实时推流采用 **WebSocket**；Agent 本地离线缓存采用轻量嵌入式存储（纯 Go SQLite / WAL 队列）。 |
| **算法运行时（Runtime）** | **Python Runtime（PyTorch / ONNX Runtime / TensorRT 等）** | 用户确认。实现约定的 **Runtime gRPC 协议**即可接入；一个计算主机可运行多个不同 `runtime_key` 的 Runtime 进程，单 Runtime 支持多任务并发，拉流解码共享由 Runtime 自主实现。 |
| **Web 前端与网关** | **Vue Pure Admin 精简国际版（`pure-admin-thin` `i18n`）+ Nginx 独立托管** | 用户确认。Vue 3 + TS + Vite + Element Plus + Pinia + Tailwind + `vue-i18n`，不内嵌 Go；Nginx 托管 `dist/` 并统一反向代理 `/api/`、`/ws/`、`/whep/` 及可选 HTTPS/WSS。 |
| **视频与画框组件** | **独立原生组件：`RTCPeerConnection` (WHEP) + 实时 Canvas 2D 叠加 + SVG/Konva ROI 编辑器** | 我方定稿。与中后台模板彻底解耦，确保低延迟硬解播放、60fps 高频画框和流畅的 ROI 拖拽编辑。 |
| **平台侧辅助媒体工具** | **独立外部 `ffmpeg` CLI（仅平台按需使用）** | 我方定稿。Go 无 CGO；算力机 `aegis-agent` 不再需要 `ffmpeg`；仅平台侧在开启“短视频证据 MP4 切片”、黑盒兜底单帧抓拍或 H.265 可选转码时按需调用。 |
| **部署交付** | **Docker Compose + 裸机双形态** | 用户确认。全离线内网交付，平台与算力机解耦部署。 |

---

## 4. 备选方案对比与选型结论（Approaches）

| 维度 | 方案 A：MediaMTX 旁路解耦 + 前端 Canvas 叠加（已选） | 方案 B：平台集中抽帧分发 + 无状态算法 API | 方案 C：服务端 GPU 流水线一体化画框推流（DeepStream 等） |
| :--- | :--- | :--- | :--- |
| **延迟表现** | **极低**（WebRTC 零转码直通浏览器，Runtime 直连授权内部 RTSP） | 较低（多了平台集中解码→跨机传图一跳） | 较低（重编码仍引入编码延迟） |
| **原始画面 + 叠加绘制** | **最灵活**（前后端分离，一键开关原始/叠加/ROI，零额外带宽） | 灵活（同 A） | 差（框烧死在流中，双流开销翻倍） |
| **算法版本与 Runtime 生态** | **极佳**（按 `runtime_key` 隔离多版本 Python/PyTorch/ONNX 环境，Runtime 自主决定多任务共享解码） | 平台解码易成瓶颈，且不适合时序动作算法 | 强绑定硬件与模型格式，难以接入异构 Python 算法 |
| **主要权衡** | 视频与检测框双通道传输，高速运动时前端可能有数帧微弱错位 | 平台解码负载重、延迟略高 | 绑定硬件框架、画面不可灵活切换 |

---

## 5. 系统架构与进程总览

```mermaid
flowchart LR
    subgraph Site["本地内网摄像头"]
        CAM["IPC 摄像头 (主/子码流 RTSP)"]
    end

    subgraph Media["媒体网关层"]
        MTX["MediaMTX\n(RTSP 中继 / WebRTC WHEP / 授权内部 RTSP / 外部鉴权)"]
    end

    subgraph Nodes["计算主机集群（1~N 台，每机 1 个纯 Go aegis-agent）"]
        subgraph Host1["计算主机 1"]
            AG1["Go aegis-agent\n(主动连平台 / 按 Runtime 键启停 / 租约续期 / 分流缓存上报)"]
            RT1["Python Runtime A (runtime_key=yolo-v2)\n可承载多任务 / 自主拉流解码推理"]
            RT2["Python Runtime B (runtime_key=action-v1)\n可承载多任务 / 自主拉流解码推理"]
            AG1 <-->|本机 gRPC: 任务/ROI/参数/租约/结果流| RT1
            AG1 <-->|本机 gRPC: 任务/ROI/参数/租约/结果流| RT2
        end
    end

    subgraph GoBackend["Go 平台后端（5 个独立进程，均支持多实例/高可用）"]
        API["aegis-api (1~N)\nREST API / 算法版本 / RBAC / 流鉴权 / 签名证据代理"]
        SCHED["aegis-scheduler (主备选主)\n任务编排 / 版本调度 / 租约管理 / MediaMTX 同步"]
        INGEST["aegis-ingest (1~N)\nAgent 主动 gRPC 接入 / 指令透传 / 内存规则引擎"]
        WS["aegis-ws (1~N)\n浏览器 WebSocket 实时推送网关"]
        DISP["aegis-dispatcher (1~N)\nNotifier 插件化报警分发 + 异步短视频证据切片"]
    end

    subgraph Infra["数据、存储与消息中间件"]
        PG[("PostgreSQL\n主业务库")]
        RD[("Redis\n热状态 / 选主锁 / Agent路由 / PubSub 实时框")]
        RMQ[["RabbitMQ\n报警队列+DLX / 证据录制队列 / 调度指令 / 配置广播"]]
        S3[("S3 对象存储 (MinIO)\n异常抓拍图 JPEG + 短视频证据 MP4")]
    end

    subgraph EdgeWeb["Web 入口与外部系统"]
        NGX["Nginx\n(托管 Vue Pure Admin dist/ + 统一反代)"]
        WEB["Web 浏览器\n(WebRTC 原始画面 + Canvas 叠加 + 报警证据回放)"]
        WH["Webhook 接收端 / 外部设备"]
        EXT["未来扩展通道 (MQTT / Modbus / 声光 / 短信)"]
    end

    CAM -->|RTSP| MTX
    MTX -->|WebRTC 媒体流| WEB
    MTX -->|授权内部 RTSP (默认低延迟)| RT1
    MTX -->|授权内部 RTSP (默认低延迟)| RT2

    WEB -->|HTTP/WS/WHEP| NGX
    NGX -->|/api & /evidence| API
    NGX -->|/ws| WS
    NGX -->|/whep| MTX

    MTX -->|externalAuthenticationURL| API
    API <--> PG
    API <--> RD
    API <--> S3
    API -->|配置/版本变更事件| RMQ

    SCHED <--> PG
    SCHED <--> RD
    SCHED -->|Control API 同步路径| MTX
    SCHED -->|经 RabbitMQ 定向发送任务/租约指令| RMQ
    RMQ -->|路由到持有 Agent 连接的实例| INGEST

    AG1 <-->|主动建立 gRPC 双向长连接| INGEST
    INGEST <--> PG
    INGEST <--> RD
    INGEST -->|上传触发抓拍图 JPEG| S3
    INGEST -->|Redis PubSub 亚毫秒实时框与告警| RD
    RD -->|PubSub 订阅| WS
    WS -->|WebSocket| WEB

    INGEST -->|报警消息 + 可选视频证据录制任务| RMQ
    RMQ -->|竞争消费 + DLX 延迟重试| DISP
    DISP -->|异步截取短视频上传 MP4| S3
    DISP -->|写投递日志 & 回填视频 Key| PG
    DISP -->|Webhook + 签名证据 URL| WH
    DISP -.->|插件扩展| EXT
    WH -.->|签名拉取图片/视频证据| API
```

---

## 6. 配套详细设计文档索引

- **数据库与 S3 存储详细设计**：`docs/design/01-database-and-storage.md`
- **gRPC / REST / WebSocket / RabbitMQ / Redis / Webhook 协议规范**：`docs/design/02-protocols-and-apis.md`
- **部署拓扑、Nginx / MediaMTX 配置与多实例运维手册**：`docs/design/03-deployment-and-ops.md`
- **Go 工程规范与仓库目录结构指南**：`docs/design/04-go-coding-standards.md`
- **分阶段工程实施计划**：`docs/superpowers/plans/2026-11-26-phase1-implementation-plan.md`
