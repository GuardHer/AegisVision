# AegisVision 企业级工厂安全与 SOP 智能监测平台重构设计规范

- **日期**：2026-11-27
- **状态**：Approved（已批准）
- **适用仓库**：`AegisVision`（`backend/`、`web/`、`runtime/`、`deploy/`）
- **关联基线文档**：[`DESIGN.md`](../../../DESIGN.md)、[`AGENTS.md`](../../../AGENTS.md)、[`docs/design/01-database-and-storage.md`](../../design/01-database-and-storage.md)、[`docs/design/02-protocols-and-apis.md`](../../design/02-protocols-and-apis.md)

---

## 1. 背景与目标（Intent & Why）

Phase 1 已跑通 6 进程纯 Go 后端、Python Runtime SDK 与离线 Vue Pure Admin 的核心闭环，但当前前后端仅处于“功能骨架 / Demo”水平：前端仅有 8 个扁平 CRUD/列表页，后端缺少空间层级、布防排班、多步骤 SOP 规则、统计分析、误报样本回流与操作审计能力。

本次重构面向**工厂生产现场的 EHS 安全行为监测（打闹、跌倒、吸烟等）与产线工位 SOP 合规监测**，将 AegisVision 升级为可直接交付工厂值班室、安全员、车间主管与算法运维团队使用的**单厂区多车间一体化企业级平台**。

### 1.1 目标（Goals）
1. **空间组织与车间级数据隔离**：建立「厂区 → 车间 → 产线/工位」多级点位树，支持按车间授权可见范围，所有监控、告警、统计均围绕车间与工位组织。
2. **值班室级实时监控体验**：提供运营态势大盘与专业监控值班中心（左侧点位树、预置轮巡方案、1/4/9/16 分屏、全屏、本地截帧、高危声光/弹窗提醒、工位实时 SOP 步骤进度条、一键快处）。
3. **长远可扩展的多步骤 SOP 规则引擎**：在现有 `anomaly_detect`、`count_threshold`、`absence_detect` 基础上，新增第 4 类规则模式 **`sop_sequence`（工位多步骤 SOP 时序流程编排器）** 与**可复用布防排班模板（白班/夜班/停线/节假日例外）**。
4. **告警处置闭环与算法迭代闭环**：支持告警多维检索、批量处置、误报原因分类、证据包（ZIP/CSV）导出，以及从误报记录自动汇聚**误报样本库**并导出图像+标注 JSON 反哺模型训练。
5. **企业级运维与治理**：多通道联动适配（Webhook / 企微 / 钉钉 / 声光报警盒 HTTP）、按严重度与车间路由、离线 ECharts 统计分析报表、全链路操作审计日志与系统健康总览。

### 1.2 非目标（Non-Goals）
- 不做跨厂区多租户 SaaS 计费或独立数据库隔离（单厂区多车间架构）。
- 不做重型多级整改工单审批流（派单→现场整改拍照→多级验收→SLA 升级）；告警采用轻量闭环（`unhandled` → `confirmed` / `false_positive` / `ignored`）。
- 不引入额外 OLAP / BI 中间件（如 ClickHouse、Elasticsearch）；单厂区统计直接基于 PostgreSQL 索引聚合。
- 不开发原生移动端 App。

### 1.3 反目标（Anti-Goals）
- 严禁违背 `AGENTS.md` 7 条红线：不得引入 CGO、不得 `go:embed` 前端、不得服务端烧录画框、不得将瞬态检测框写入 RabbitMQ/SQLite、不得因 MP4 录制阻塞告警、不得在断流/离线时触发离岗或 SOP 超时误报、不得引入任何外网 CDN 资源。
- 严禁将值班高频操作（确认告警、标误报、切换分屏/轮巡）藏进多层弹窗。

---

## 2. 架构约束与已选推进方案（Constraints & Approaches）

### 2.1 硬性约束
- 保持 `backend/` 6 个进程边界与包隔离（`api`、`scheduler`、`ingest`、`ws`、`notifier`/`evidence`、`agent` 互不直接 `import`，仅 `ingest` 调用纯内存包 `rules`）。
- 全部数据库演进通过 `backend/migrations/`（`goose`）增量迁移，查询经 `sqlc` 生成；本地 Go Module 保持 `aegisvision/backend`。
- 前端基于 `web/`（Vue Pure Admin `i18n`），全部新增文案同步维护 `web/locales/zh-CN.yaml` 与 `web/locales/en.yaml`，图表使用已内置的离线 `echarts` v6。

### 2.2 候选方案对比与决策

| 方案 | 描述 | 取舍 | 结论 |
| :--- | :--- | :--- | :--- |
| **方案 A（已选）** | 沿用现有 6 进程纯 Go + PostgreSQL 架构，一次性完成全套企业级表结构、协议与 14 页蓝图设计，按 **M1 / M2 / M3 三个可独立验收里程碑**分期交付 | 每个里程碑均可直接部署真机验收，风险最低，表结构与接口一次规划到位不返工 | **采用** |
| **方案 B** | 一次性开发完全部后端表与接口，再统一重写全部 14 个前端页面 | 中间周期长、无可视页面反馈，SOP 编排与大盘交互若需微调返工成本大 | 放弃 |
| **方案 C** | 拆出独立 Analytics/BI 服务并引入 ClickHouse/时序库做报表 | 适合日均百万级告警的超大型集团；单厂区离线部署运维负担过重 | 放弃 |

---

## 3. 企业级信息架构与 14 个页面规划

默认首页 `/` 路由由原 `/monitor` 调整为 **`/dashboard`（运营态势总览）**，左侧菜单重构为 **6 大业务域 / 14 个页面**：

```text
AegisVision 企业控制台
├── 1. 运营总览 (/dashboard)                    [admin, operator, viewer]
├── 2. 实时值班中心 (/monitor)                  [admin, operator, viewer]
├── 3. 告警与统计
│    ├── 告警处置台 (/alarms)                   [admin, operator, viewer(只读)]
│    └── 统计分析报表 (/analytics)              [admin, operator, viewer]
├── 4. 点位与布防策略
│    ├── 厂区点位管理 (/cameras)                [admin]
│    ├── 点位智能配置 (/cameras/:id/config)     [admin, 隐藏菜单]
│    └── 布防排班模板 (/schedules)              [admin]
├── 5. 算法与算力运维
│    ├── 算法仓库 (/algorithms)                 [admin]
│    ├── 误报样本库 (/datasets)                 [admin, operator]
│    └── 算力集群与任务 (/nodes)                [admin]
└── 6. 联动与系统治理
     ├── 联动通知通道 (/channels)               [admin]
     ├── 用户与车间权限 (/users)                [admin]
     ├── 操作审计日志 (/audit-logs)             [admin]
     └── 系统设置与健康 (/settings)             [admin]
```

### 各页面核心功能规范

1. **运营态势总览（`/dashboard`）**
   - **顶部 KPI 卡**：今日告警总数（环比昨日）、待处理告警数（点击跳转 `/alarms?handle_status=unhandled`）、今日确认违规率 / 误报率、今日 SOP 违规次数、摄像头在线率（在线/总数）、算力节点在线率与运行任务数。
   - **可视化图表（离线 ECharts）**：近 7 日安全行为 vs SOP 违规趋势折线/柱状图；各车间告警排行 Top 10 横向柱状图；违规行为类型占比环形图。
   - **实时高危动态**：最近 10 条未处理 `critical`/`warning` 告警列表，支持直达详情或一键确认。
2. **实时值班中心（`/monitor`）**
   - **左栏（可折叠）**：Tab 1「点位树」（厂区 → 车间 → 产线/工位树，显示摄像头在线绿/灰状态点、搜索过滤、点击或拖拽上墙）；Tab 2「轮巡方案」（保存/切换常用 4/9/16 分屏方案，支持开启定时轮巡，如每 30s 自动翻页）。
   - **中栏**：工具栏（1/4/9/16 分屏切换、主/子码流切换、画框开关、全局告警音静音开关、全屏切换）；每个视频格提供画面名称、工位号、本地 Canvas 抓拍下载、以及当该点位配置了 `sop_sequence` 规则时在底部叠加**工位 SOP 实时步骤进度条**（高亮已完成/当前步骤/异常步骤）。
   - **右栏（可折叠）**：按严重度筛选的实时告警卡片流（含抓拍缩略图、车间/工位、时间），支持值班员直接在卡片上点击「确认」「误报」「查看详情」；当收到 `critical` 告警且未静音时触发浏览器提示音（使用本地内置 Web Audio 合成提示音，零外部音频依赖）与非阻塞通知弹窗。
3. **告警处置台（`/alarms`）**
   - **多维筛选**：车间/产线级联选择、摄像头、规则模式（含 `sop_sequence`）、严重度、处置状态（`unhandled`/`confirmed`/`false_positive`/`ignored`）、时间范围、关键字。
   - **批量操作与导出**：勾选多条告警执行「批量确认」「批量标误报（可选统一误报原因）」「批量忽略」；支持按当前筛选条件导出 **CSV 明细** 或 **证据 ZIP 包**（含抓拍原图 + `metadata.json`，单次上限 500 条流式打包）。
   - **详情抽屉**：抓拍图 + 归一化检测框/ROI 叠加（自适应真实尺寸）、异步 MP4 切片播放与下载、**SOP 违规步骤时间线**（当 `rule_mode=sop_sequence` 时展示每步完成时间、耗时与缺失/乱序/超时节点）、处置表单（状态 + 误报原因下拉 + 备注）、通道投递明细与一键重试。
4. **统计分析报表（`/analytics`）**
   - 支持按时间区间（今日 / 近 7 天 / 近 30 天 / 自定义）、车间、班次时段筛选。
   - 展示：告警趋势（按天/按小时分布）、各车间安全违规与 SOP 违规对比、**SOP 高频违规步骤排行**（定位哪一步最容易漏做或超时）、**算法版本误报率对比表**（总命中 / 确认 / 误报 / 误报率），支持一键导出统计汇总 CSV。
5. **厂区点位管理（`/cameras`）**
   - 左右分栏：左侧为「厂区 → 车间 → 产线/工位」树管理（新增子节点、重命名、排序、删除校验）；右侧为选中节点（含下级）的摄像头表格，展示工位编码、主/子码流状态、最近在线时间、已启用规则数。
   - 支持批量启用/停用、批量移入指定车间、CSV 批量导入摄像头、快捷入口跳转「点位智能配置」。
6. **点位智能配置工作台（`/cameras/:id/config`）**
   - **上半区（视觉画布）**：实时 WHEP 画面（支持截图定格方便画框）+ 多 ROI 协同 SVG 编辑器（支持为每个 ROI 设不同颜色、选中高亮、切换显隐、右键删点、拖拽修正、全画面 ROI）。
   - **下半区（规则配置）**：支持全部 4 种规则模式：
     - `anomaly_detect`（安全异常：打闹、跌倒、吸烟等）；
     - `count_threshold`（区域人数上下限）；
     - `absence_detect`（工位离岗 + 心跳断流防误报超时）；
     - **`sop_sequence`（可视化多步骤 SOP 编排器）**：有序步骤卡片列表（支持拖拽/上移下移排序），每步配置步骤名称、目标标签、绑定 ROI（同摄像头下任意已启用 ROI 或继承主 ROI）、确认帧数、最大等待超时（秒）、是否必做；全局配置触发方式、单周期总超时、违规类型勾选（漏步 / 乱序 / 超时）。
   - 所有规则统一支持选择**布防排班模板**（默认 24×7）、调度策略（自动 / 指定节点）、是否异步录 MP4 及前/后秒数、联动通道多选。
7. **布防排班模板（`/schedules`）**
   - 维护可复用的时间模板（如“白班 08:00-20:00”、“夜班 20:00-08:00 跨午夜”、“工作日生产时段”），支持可视化周时段网格（周一至周日多时段）+ 节假日/停线排除日期列表（`exclude_dates`）+ 临时加班生效日期（`include_dates`），显示每个模板当前关联的规则数。
8. **算法仓库（`/algorithms`）**
   - 算法目录与多版本展开管理，展示每版本支持的标签集合、绑定 `runtime_key`、生命周期状态（`draft`/`active`/`deprecated`），并关联展示近 30 天该版本的**线上触发数、确认数与误报率**，辅助安全与算法团队决定灰度切换或版本下线。
9. **误报样本库（`/datasets`）**
   - 自动聚合 `handle_status = 'false_positive'` 且带抓拍图的告警记录，支持按算法版本、车间、误报原因（如光线反光 / 遮挡 / 动作相似 / 标注漂移 / 其他）、时间段筛选与卡片式预览。
   - 支持一键导出**离线训练样本包 ZIP**（包含 `images/*.jpg` + `annotations.json`，内含归一化 `bbox`、`label`、`confidence`、`roi`、`false_positive_reason`、`handle_note`），方便算法团队直接导入标注平台做负样本微调。
10. **算力集群与任务监控（`/nodes`）**
    - 节点卡片/表格展示主机名、IP、支持 `runtime_key`、任务负载（运行/上限），并从 `agent_nodes.meta` 可视化展示 **CPU / 内存 / GPU 利用率与显存水位进度条**（未上报 GPU 字段时优雅降级显示“无 GPU 指标”），支持节点一键排空（`draining`）与恢复上线（`online`）。
    - 下方推理任务表展示任务所属车间/摄像头、算法版本、租约到期倒计时、最近帧时间（`last_frame_at`）、运行状态与错误信息，支持一键重调度。
11. **联动通知通道（`/channels`）**
    - 统一管理 4 类工业现场常用通道：`webhook`（通用 HMAC 签名）、`wecom`（企业微信群机器人）、`dingtalk`（钉钉加签机器人）、`sound_light`（现场网络声光报警盒 HTTP 触发器，支持配置触发/复位 URL 与 JSON 模板）。
    - 每个通道支持配置**路由过滤条件**（可选按 `severities`、`zone_ids`、`rule_modes` 过滤，空表示匹配全部），并提供连通性测试与最近投递记录查看。
12. **用户与车间权限（`/users`）**
    - 管理用户角色（`admin`/`operator`/`viewer`）、启停与重置密码；新增**数据范围授权**：开关「全厂区可见」或勾选指定车间/产线节点（授权父节点自动继承全部下级节点）。
13. **操作审计日志（`/audit-logs`）**
    - 只读分页表格，按操作人、动作类型、资源类型（camera / zone / roi / rule / schedule / algorithm / channel / user / node / alarm / settings）、时间段检索，展示操作摘要、客户端 IP、Request ID 与时间戳。
14. **系统设置与健康（`/settings`）**
    - 全局运营配置：默认告警提示音开关、弹窗严重度阈值、证据导出单次上限、默认时区（`Asia/Shanghai`）、前端平台标题/工厂名称定制。
    - 服务与存储健康面板：展示 `aegis-api`、PostgreSQL、Redis、RabbitMQ、MinIO/S3 桶保留策略配置状态及 MediaMTX 连通状态。

---

## 4. 数据库增量设计（`migrations/00002_enterprise_features.sql`）

全部新表沿用 UUIDv7 主键、`TIMESTAMPTZ`、`JSONB` 与外键约束；对既有表只做向后兼容的增量扩展（所有新列提供安全默认值，不破坏 Phase 1 既有数据）。

### 4.1 新增表

1. **`zones`（厂区 → 车间 → 产线/工位树）**
   ```sql
   CREATE TABLE zones (
       id UUID PRIMARY KEY,
       parent_id UUID NULL REFERENCES zones(id) ON DELETE RESTRICT,
       name VARCHAR(128) NOT NULL,
       code VARCHAR(64) NOT NULL DEFAULT '',
       zone_type VARCHAR(32) NOT NULL CHECK (zone_type IN ('factory', 'workshop', 'line', 'workstation')),
       sort_order INT NOT NULL DEFAULT 0,
       description TEXT NOT NULL DEFAULT '',
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   CREATE INDEX idx_zones_parent_id ON zones (parent_id, sort_order);
   ```
   - 迁移时默认不强制插入脏数据；若前端首次打开 `zones` 为空，管理员可一键创建顶层厂区节点（或按需创建车间）。

2. **`user_zones`（用户车间数据权限映射）**
   ```sql
   CREATE TABLE user_zones (
       user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
       zone_id UUID NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
       PRIMARY KEY (user_id, zone_id)
   );
   ```
   - 配合 `users.all_zones_access BOOLEAN NOT NULL DEFAULT TRUE`：
     - `admin` 永远拥有全厂区访问权；
     - `operator`/`viewer` 当 `all_zones_access = TRUE` 时拥有全厂区可见范围（保证升级后既有账号行为兼容）；当管理员关闭 `all_zones_access` 时，仅允许访问 `user_zones` 及其全部递归子节点下的摄像头、监控流、告警、统计与样本。

3. **`arming_schedules`（布防排班模板）**
   ```sql
   CREATE TABLE arming_schedules (
       id UUID PRIMARY KEY,
       name VARCHAR(128) NOT NULL UNIQUE,
       description TEXT NOT NULL DEFAULT '',
       timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
       weekly_windows JSONB NOT NULL DEFAULT '[]'::jsonb,
       exclude_dates JSONB NOT NULL DEFAULT '[]'::jsonb,
       include_dates JSONB NOT NULL DEFAULT '[]'::jsonb,
       enabled BOOLEAN NOT NULL DEFAULT TRUE,
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   ```
   - `weekly_windows` JSONB 结构：`[{"days":[1,2,3,4,5],"start":"08:00","end":"20:00"}]`（`days` 为 `1=周一 ... 7=周日`；当 `end < start` 如 `"start":"20:00","end":"08:00"` 时表示跨午夜夜班，自动覆盖当日 `20:00-24:00` 及次日 `00:00-08:00`）。
   - `[架构裁决]` `exclude_dates`：`["2026-10-01", "2026-10-02"]`，按帧时间戳在模板时区下的**自然日历日**判定（例如标注 `2026-10-01` 放假时，前一天 `20:00` 开始的夜班在 `2026-10-01 00:00` 立即撤防）。
   - `[架构裁决]` `include_dates`：`[{"date":"2026-10-03","start":"08:00","end":"20:00"}]`（省略 `start`/`end` 时默认全天 `00:00-24:00`）。优先级：`exclude_dates` > `include_dates` > `weekly_windows`。

4. **`sop_cycles`（SOP 周期执行结果表，用于精确计算 SOP 合规率）** `[架构裁决]`
   ```sql
   CREATE TABLE sop_cycles (
       id UUID PRIMARY KEY,
       rule_id UUID NOT NULL REFERENCES roi_algorithm_rules(id) ON DELETE CASCADE,
       camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
       zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL,
       algorithm_version_id UUID NULL REFERENCES algorithm_versions(id) ON DELETE SET NULL,
       outcome VARCHAR(16) NOT NULL CHECK (outcome IN ('compliant', 'violated')),
       violation_type VARCHAR(32) NOT NULL DEFAULT '',
       alarm_id UUID NULL REFERENCES alarms(id) ON DELETE SET NULL,
       started_at TIMESTAMPTZ NOT NULL,
       ended_at TIMESTAMPTZ NOT NULL,
       duration_ms INT NOT NULL DEFAULT 0,
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   CREATE INDEX idx_sop_cycles_zone_time ON sop_cycles (zone_id, ended_at DESC);
   CREATE INDEX idx_sop_cycles_rule_time ON sop_cycles (rule_id, ended_at DESC);
   ```
   - 仅在每个 SOP 周期闭环完成（`compliant`）或触发违规（`violated`）时写入 1 行（非逐帧写入），支撑 `/dashboard` 与 `/analytics` 精确计算 `SOP 合规率 = compliant / (compliant + violated)` 与平均作业周期时长。

5. **`monitor_presets`（实时监控轮巡/分屏预设方案）**
   ```sql
   CREATE TABLE monitor_presets (
       id UUID PRIMARY KEY,
       owner_user_id UUID NULL REFERENCES users(id) ON DELETE CASCADE,
       name VARCHAR(128) NOT NULL,
       is_shared BOOLEAN NOT NULL DEFAULT FALSE,
       grid_size INT NOT NULL DEFAULT 4 CHECK (grid_size IN (1, 4, 9, 16)),
       camera_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
       tour_interval_seconds INT NOT NULL DEFAULT 0,
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   CREATE INDEX idx_monitor_presets_owner ON monitor_presets (owner_user_id, is_shared);
   ```

6. **`audit_logs`（企业操作审计日志）**
   ```sql
   CREATE TABLE audit_logs (
       id UUID PRIMARY KEY,
       actor_user_id UUID NULL REFERENCES users(id) ON DELETE SET NULL,
       actor_username VARCHAR(64) NOT NULL DEFAULT '',
       action VARCHAR(64) NOT NULL,
       resource_type VARCHAR(64) NOT NULL,
       resource_id VARCHAR(128) NOT NULL DEFAULT '',
       summary TEXT NOT NULL DEFAULT '',
       detail JSONB NOT NULL DEFAULT '{}'::jsonb,
       client_ip VARCHAR(64) NOT NULL DEFAULT '',
       request_id VARCHAR(64) NOT NULL DEFAULT '',
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   CREATE INDEX idx_audit_logs_created_at ON audit_logs (created_at DESC);
   CREATE INDEX idx_audit_logs_resource ON audit_logs (resource_type, created_at DESC);
   CREATE INDEX idx_audit_logs_actor ON audit_logs (actor_username, created_at DESC);
   ```

7. **`system_settings`（全局系统设置 KV）**
   ```sql
   CREATE TABLE system_settings (
       key VARCHAR(64) PRIMARY KEY,
       value JSONB NOT NULL DEFAULT '{}'::jsonb,
       updated_by UUID NULL REFERENCES users(id) ON DELETE SET NULL,
       updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
   );
   ```

### 4.2 既有表增量字段（`ALTER TABLE`）
- `users`：新增 `all_zones_access BOOLEAN NOT NULL DEFAULT TRUE`。
- `cameras`：新增 `zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL`、`workstation_code VARCHAR(64) NOT NULL DEFAULT ''`、`tags JSONB NOT NULL DEFAULT '[]'::jsonb`、`last_online_at TIMESTAMPTZ NULL`；新增索引 `idx_cameras_zone_id (zone_id)`。
- `camera_rois`：新增 `color VARCHAR(16) NOT NULL DEFAULT '#38bdf8'`。
- `roi_algorithm_rules`：
  - 更新 `rule_mode` CHECK 约束，允许 `('anomaly_detect', 'count_threshold', 'absence_detect', 'sop_sequence')`；
  - 新增 `schedule_id UUID NULL REFERENCES arming_schedules(id) ON DELETE SET NULL`。
- `alarms`：
  - 新增 `zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL`（落库时冗余写入摄像头当前 `zone_id`，确保车间维度筛选与统计免多次 Join 且摄像头换区后历史归属不变）；
  - 新增 `false_positive_reason VARCHAR(64) NOT NULL DEFAULT ''`（如 `lighting`、`occlusion`、`similar_action`、`model_drift`、`other`）；
  - 新增 `sop_trace JSONB NULL`（仅 `sop_sequence` 告警写入结构化步骤执行轨迹）；
  - `handle_status` 允许 `unhandled`、`confirmed`、`false_positive`、`ignored`；
  - 新增索引 `idx_alarms_zone_time (zone_id, triggered_at DESC)`、`idx_alarms_algo_handle (algorithm_version_id, handle_status, triggered_at DESC)`。
- `alarm_channels`：新增 `routing_rules JSONB NOT NULL DEFAULT '{"auto_route":false}'::jsonb`（`{"auto_route": false, "severities": [], "zone_ids": [], "rule_modes": []}`）。`[架构裁决]` 默认 `auto_route=false` 保证既有 Phase 1 通道及按规则绑定的通道不会意外向全厂广播所有告警。

---

## 5. 核心后端机制设计

### 5.1 多步骤 SOP 状态机（`internal/rules/sop.go`）

`sop_sequence` 运行在 `aegis-ingest` 内存规则引擎的按 `camera_id` 分片锁内（**零 IO、零全局锁**），支持同一摄像头内跨多个 ROI 的有序动作检验。

**`params` JSONB 契约（`rule_mode = 'sop_sequence'`）**：
```json
{
  "trigger_mode": "first_step",
  "entry_labels": ["person"],
  "rearm_mode": "exit_or_cooldown",
  "rearm_cooldown_seconds": 30,
  "cycle_timeout_seconds": 120,
  "heartbeat_timeout_seconds": 5,
  "cooldown_seconds": 30,
  "severity": "warning",
  "violation_types": ["missing_step", "out_of_order", "step_timeout"],
  "steps": [
    {
      "step_id": "step_1",
      "name": "佩戴防静电手环",
      "labels": ["wear_esd_strap"],
      "roi_id": "",
      "min_confidence": 0.6,
      "hit_point": "center",
      "min_dwell_frames": 2,
      "max_wait_seconds": 15,
      "required": true
    },
    {
      "step_id": "step_2",
      "name": "扫码登记工单",
      "labels": ["scan_barcode"],
      "roi_id": "<optional-other-roi-on-same-camera>",
      "min_confidence": 0.6,
      "hit_point": "center",
      "min_dwell_frames": 1,
      "max_wait_seconds": 20,
      "required": true
    }
  ]
}
```

**状态流转与防误报约束（已确认）**：
1. **单工位单周期追踪模型**：每条 `sop_sequence` 规则同一时刻维护 **1 条主 SOP 状态机**（适配典型单操作员工位，前端监控格底部渲染单一清晰进度条），不按 `track_id` 拆多条并发周期。
2. **初始态 `idle` 与防连环重触发**：
   - `trigger_mode = "first_step"`：命中第 0 个必做步骤（或前置可选步骤）且持续满 `min_dwell_frames` 时启动新周期进入 `in_progress`；若直接命中后续步骤跳过前置必做步骤且启用了 `out_of_order` / `missing_step`，立即判定违规并输出 `sop_trace`。
   - `trigger_mode = "zone_entry"`：在主 ROI 检测到 `entry_labels`（默认 `["person"]`）即开启周期并计时第 0 步；一次周期完成或超时/违规复位后，进入 `rearm` 保护期——需人员**离开主 ROI 后再次进入**或经过 `rearm_cooldown_seconds`（默认 30s）才允许开启下一周期，防止工人在工位静止时连续触发超时误报。
3. **进行态 `in_progress` 与可选步骤语义**：
   - 维护 `current_step_idx`、`step_elapsed`、`cycle_elapsed`、`last_heartbeat_at` 与 `completed_steps[]`。
   - **可选步骤（`required: false`）**：若操作员直接完成后续步骤，跳过的所有 `required: false` 步骤静默略过（不触发 `missing_step` / `out_of_order`），且 `required: false` 步骤不触发单步 `step_timeout`。
   - **断流/离线防误报红线**：与 `absence_detect` 一致，只有当两帧间隔 `0 <= gap <= heartbeat_timeout_seconds`（默认 5s）时才推进 `step_elapsed` 与 `cycle_elapsed`；若超过心跳超时则暂停/重置当前计时，严禁在摄像头断流或算力节点宕机时误报“步骤超时”。
   - 命中期望的下一步且达到 `min_dwell_frames` → 记录该步完成时间戳与耗时，推进到下一步；若全部必做步骤完成 → 周期正常闭环，异步写入一条 `sop_cycles(outcome='compliant')` 并复位为 `idle`。
   - 命中靠后的步骤（跳过中间任意 `required: true` 步骤）→ 若启用 `missing_step`/`out_of_order`，触发违规报警（写 `alarms` + `sop_cycles(outcome='violated')`）并附完整 `sop_trace`，复位周期。
   - 必做步骤 `step_elapsed > max_wait_seconds` 或 `cycle_elapsed > cycle_timeout_seconds` → 若启用 `step_timeout`，触发超时报警并附 `sop_trace`，复位周期。
4. **实时进度广播**：`EvalOutput` 新增 `SOPProgress []SOPStepProgress`（含 `rule_id`、`rule_name`、`state`、`current_step_index`、`steps_status`），随 `frame_detections` 瞬态消息经 Redis Pub/Sub → `aegis-ws` 推送至监控墙渲染底部实时 SOP 步骤进度条（**绝不落库或进 RabbitMQ**）。
5. **无图状态类告警的单帧抓拍兜底（已确认）**：当 `sop_sequence`、`absence_detect`、`count_threshold` 触发告警且算法帧未附带 `snapshot_jpeg` 时，`aegis-ingest` 仍立即落库并推送实时告警与通知，同时向 RabbitMQ `evidence.record` 投递异步单帧抓拍任务（`mode: "snapshot"`）；由 `aegis-dispatcher` 签发短期 RTSP Pull Token 并调用外部 `ffmpeg` CLI（`-frames:v 1 -q:v 3`）从 MediaMTX 抓取一帧 JPEG 上传 MinIO 并回填 `alarms.image_object_key`，严禁阻塞报警主链路。

### 5.2 布防排班判定（`internal/rules/schedule.go`）
- `aegis-ingest` 内存规则热重载时同步加载 `arming_schedules` 到每个 `Rule.Schedule`。
- `Engine.Evaluate(frame)` 在评估单条规则前先判定 `rule.Schedule.IsArmed(frame.Timestamp)`：
  - 若处于撤防时段：跳过告警触发并清空瞬态累积状态（如 `consecutiveHits`、`absentElapsed`、进行中 SOP 计时），避免刚切入布防时用撤防期间的残余状态误触发；
  - 实时检测框 `frame_detections` 依然正常广播，保证监控墙画面实时画框不受撤防影响。

### 5.3 车间数据权限隔离（`internal/api/scope.go` & `aegis-ws`）`[架构裁决]`
- 新增权限解析辅助器 `ResolveAllowedZoneIDs(ctx, store, claims)`：
  - `role == "admin"` 或用户 `all_zones_access == true` → 返回 `unrestricted = true`；
  - 否则一次查出 `zones` 树与 `user_zones`，在内存做 BFS 展开所有授权节点及子孙节点 ID 集合。
- **REST API 隔离**：覆盖摄像头列表/详情/流 Token、告警列表/详情/处置/证据代理、统计分析、误报样本库与监控轮巡。
- **`aegis-ws` 实时隔离与在线吊销**：
  - `aegis-ws` 接入只读 PostgreSQL（复用现有 `config.Config.Postgres.DSN`），以 Redis `user:scope:<user_id>`（TTL 5m）作为热缓存、cache miss 时查库回填，不与 `aegis-api` 发生 HTTP 耦合；
  - `wsAlarmAlertMessage` 补充 `zone_id` 字段；`ws.Hub` 为每个已连客户端保存 `userID`、`unrestricted` 与 `allowedCameraIDs` / `allowedZoneIDs`，同时过滤 `BroadcastFrame` 与 **`BroadcastAlarm`**，确保受限车间账号绝不收到其他车间的实时检测框或告警推送；
  - 当管理员修改用户车间权限或移动摄像头所属区域时，`aegis-api` 清除对应 Redis 缓存并向 Redis Pub/Sub `auth:scope_changed` 发布失效事件，`aegis-ws` 立即刷新在线客户端权限并剔除越权订阅。

### 5.4 多通道通知插件、路由过滤与声光盒复位（`internal/notifier/`）
- 在 `notifier.Registry` 新增 3 个实现 `Notifier` 接口的插件（零第三方 SDK 依赖，纯标准库 `net/http` + `crypto/hmac`）：
  1. `wecom`（企业微信群机器人 Markdown 消息，携带车间/工位/规则/时间与签名图片链接）；
  2. `dingtalk`（钉钉自定义机器人 Markdown + `timestamp\nsecret` HMAC-SHA256 Base64 加签）；
  3. `sound_light`（现场网络声光报警盒 HTTP 适配：支持配置 `trigger_url`、`reset_url`、`method`、`headers`、`body_template`、`reset_body_template`、`auto_reset_seconds`）。
- **声光报警盒复位机制（已确认组合模式）**：
  1. **自动延时复位**：当通道配置 `auto_reset_seconds > 0` 且配置了 `reset_url` 时，`aegis-dispatcher` 触发响铃成功后向 RabbitMQ 投递延时复位任务（或基于受控 `ctx` 定时器 + 队列兜底）到期自动调用 `reset_url`；
  2. **处置联动复位 & 手动消音复位**：值班员在 `/monitor` 或 `/channels` 点击「消音复位」（`POST /api/v1/channels/:id/reset`），或在告警被处置（`confirmed`/`false_positive`/`ignored`）时，`aegis-api` 通过 RabbitMQ 通道复位指令通知 `aegis-dispatcher` 立即执行 `reset_url`（保持 `internal/api` 不直接 `import internal/notifier` 的包隔离红线）。
- **通道路由（`[架构裁决]` 显式开启 + 树递归匹配）**：
  - `alarm_channels.routing_rules` 仅当显式设置 `"auto_route": true` 时参与自动路由，默认 `false` 不影响既有规则绑定；
  - 当 `"auto_route": true` 时，`aegis-ingest` 利用内存缓存的 `zones` 树递归判断告警的 `zone_id` 是否落在 `routing_rules.zone_ids` 任一父/子节点下，同时匹配 `severities` 与 `rule_modes`（各子数组为空表示该维度不限），再与 `rule_alarm_channels` 显式绑定通道按 `channel_id` 合并去重。

### 5.5 算力节点无 CGO 指标采集（`aegis-agent`）`[架构裁决]`
- M3 在 `aegis-agent` 心跳中接入真实主机与 GPU 指标采集（严守纯 Go 无 CGO 红线）：
  - CPU / 内存占用：Linux 读 `/proc/stat` 与 `/proc/meminfo`（Windows 采用标准 `syscall` / `golang.org/x/sys/windows` 查询），零 CGO；
  - GPU 指标：外部执行 `nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits`（1s 超时）；命令不存在或执行失败时标记 `has_gpu = false`，与 `0%` 负载明确区分。
  - 通过 `buf` 向后兼容扩展 `agent.proto` 的 `HostMetrics`（新增 `bool has_gpu` 与 `float gpu_temp_c` 字段），并由 `aegis-ingest` 写入 `agent_nodes.meta` 供 `/nodes` 页面渲染。

### 5.6 审计日志与流式导出（`internal/api/`）
- 审计记录助手 `recordAudit(c, store, action, resourceType, resourceID, summary, detail)`：在全部写接口成功后异步或事务外非阻塞写入 `audit_logs`（脱敏不记录密码/Token/Webhook Secret），写入失败仅打 `slog.Warn` 不阻断主业务。
- 证据 ZIP 与误报样本 ZIP 导出使用标准库 `archive/zip` 直接流式写入 `http.ResponseWriter`（`Content-Disposition: attachment`），逐条从 `storage.ObjectStorage` 拉取 JPEG 写入 zip entry 后立即释放，内存占用 O(1)，单次上限默认 500 条（可在 `system_settings` 调整，硬上限 2000）。

---

## 6. 新增与扩展 REST / WS 接口清单

| 域 | 方法与路径 | 角色 | 说明 |
| :--- | :--- | :--- | :--- |
| **空间树** | `GET /api/v1/zones/tree` | all | 返回带摄像头计数与在线数的厂区/车间/产线树（按用户数据范围过滤） |
| | `POST /api/v1/zones`、`PUT /api/v1/zones/:id`、`DELETE /api/v1/zones/:id` | admin | 空间树节点增删改（存在子区域或关联摄像头时禁止硬删） |
| **大盘与报表** | `GET /api/v1/dashboard/overview` | all | 运营总览 KPI、7 日趋势、车间 Top10、行为分布、设备与算力水位 |
| | `GET /api/v1/analytics/summary` | all | 按时间/车间/班次聚合安全违规趋势、SOP 合规率、高频违规步骤、算法误报率 |
| | `GET /api/v1/analytics/export` | all | 导出统计汇总 CSV |
| **监控轮巡** | `GET/POST /api/v1/monitor/presets`、`PUT/DELETE /api/v1/monitor/presets/:id` | all (共享需 admin) | 保存/管理分屏与定时轮巡预设方案 |
| **点位增强** | `POST /api/v1/cameras/batch`、`POST /api/v1/cameras/import` | admin | 摄像头批量启停/调区、CSV 批量导入 |
| **布防排班** | `GET/POST /api/v1/schedules`、`PUT/DELETE /api/v1/schedules/:id` | admin | 布防排班模板 CRUD（删除前校验是否被规则引用） |
| **告警增强** | `POST /api/v1/alarms/batch-handle` | admin, operator | 批量确认 / 标误报（带 `false_positive_reason`）/ 忽略 |
| | `POST /api/v1/alarms/export` | all | 按筛选条件流式导出 CSV 或证据包 ZIP |
| **误报样本库** | `GET /api/v1/datasets/false-positives` | admin, operator | 分页检索带抓拍的误报样本 |
| | `POST /api/v1/datasets/export` | admin, operator | 流式导出离线训练样本 ZIP（`images/` + `annotations.json`） |
| **联动控制** | `POST /api/v1/channels/:id/reset` | admin, operator | 手动触发声光报警盒消音复位（经 RabbitMQ 投递至 `aegis-dispatcher` 执行） |
| **用户权限** | `PUT /api/v1/users/:id/zones` | admin | 设置用户 `all_zones_access` 与授权 `zone_ids` |
| **审计与设置** | `GET /api/v1/audit-logs` | admin | 多维筛选分页查询操作审计日志 |
| | `GET/PUT /api/v1/settings`、`GET /api/v1/system/health` | admin (读 settings 为 all) | 全局设置读写、中间件/存储策略与服务健康聚合 |
| **WS 扩展** | `frame_detections` 新增可选 `sop_progress` 数组；`alarm_alert` 补充 `zone_id` | all | 实时向监控墙推送工位 SOP 步骤进度，并支持按车间严格过滤实时告警 |

---

## 7. 分阶段里程碑交付规划（方案 A）

### Milestone 1（M1）：空间组织 + 运营大盘 + 值班监控墙 + 告警处置台升级
- **数据库**：执行 `00002_enterprise_features.sql` 一次性落齐企业表结构（确保 M2/M3 零破坏性改表）。
- **后端**：`zones` 树 CRUD、摄像头批量操作与 `zone_id`/工位绑定、`monitor_presets` CRUD、`dashboard/overview` 聚合接口、告警 `ignored` 状态 + `false_positive_reason` + 批量处置 + CSV/ZIP 证据流式导出、默认路由切至 `/dashboard`。
- **前端**：全新**运营总览大盘（`/dashboard`）**、重构**实时值班中心（`/monitor`：点位树 + 轮巡 + 16 分屏 + 告警音/弹窗 + 卡片快处）**、升级**厂区点位管理（`/cameras` 左树右表）**、升级**告警处置台（`/alarms` 批量处置/误报原因/ZIP导出）**。
- **验收标准**：管理员可建厂区→车间→工位树并绑定摄像头；值班员可在 16 分屏与轮巡间切换并一键批量处置告警与导出证据 ZIP；大盘离线 ECharts 正常展示真实数据。

### Milestone 2（M2）：布防排班 + `sop_sequence` 多步骤 SOP 编排 + 单帧抓拍兜底 + 统计分析报表
- **后端**：`arming_schedules` CRUD 与内存布防判定（含跨午夜与节假日例外）；`rules.Engine` 实现单工位单周期 `sop_sequence` 状态机（含可选步骤、进区防连环重触发与断流暂停防误报）+ `sop_cycles` / `sop_trace` 落库 + WS `sop_progress` 实时广播；`aegis-dispatcher` 新增缺图单帧 `ffmpeg` JPEG 异步抓取回填；`analytics/summary`（含真实 SOP 合规率）与 CSV 导出接口；Mock Runtime 新增 `sop` 模拟模式用于 E2E 验证。
- **前端**：新增**布防排班模板（`/schedules`）**、升级**点位智能配置工作台（`/cameras/:id/config`：多 ROI 配色协同 + `sop_sequence` 可视化多步骤拖拽编排器 + 绑定排班模板）**、监控墙底部叠加**工位 SOP 实时进度条**、告警详情抽屉增加 **SOP 违规时间线**、新增**统计分析报表（`/analytics`）**。
- **验收标准**：撤防时段零误报；真实 Python Mock Runtime 驱动漏步/乱序/超时 3 类 SOP 违规均能准确报警并附 `sop_trace`，断流超时不误报；统计报表准确反映车间与 SOP 违规步骤排行。

### Milestone 3（M3）：误报样本回流 + 多通道联动与声光复位 + 车间数据权限 + 审计与治理
- **后端**：误报样本检索与训练包 ZIP 导出（`/datasets`）；`wecom` / `dingtalk` / `sound_light`（含自动延时复位、处置联动复位与 `POST /api/v1/channels/:id/reset` 手动复位）通知插件与 `routing_rules` 显式路由；`user_zones` 车间数据隔离（覆盖 REST API 与 `aegis-ws` 实时检测框/告警过滤及在线吊销）；`audit_logs` 全写操作记录与查询；`system_settings` 与 `system/health` 聚合接口；`aegis-agent` 无 CGO 采集 CPU/内存/`nvidia-smi` GPU 指标与算法版本误报率统计。
- **前端**：新增**误报样本库（`/datasets`）**、升级**算法仓库（`/algorithms` 展示版本误报率）**与**算力集群（`/nodes` 展示 CPU/内存/GPU 水位条）**、升级**联动通知通道（`/channels` 4 种模板表单 + 路由条件）**与**用户管理（`/users` 车间树授权）**、新增**操作审计日志（`/audit-logs`）**与**系统设置与健康（`/settings`）**。
- **验收标准**：受限车间账号无法查询或订阅其他车间的流、告警与统计；企微/钉钉/声光盒插件通过单元测试与 Mock HTTP 联调；误报样本 ZIP 包含有效图像与规范标注 JSON；全部写操作均生成脱敏审计日志。

---

## 8. 留给实施阶段决定的细节（Left to the Builder）
- 前端具体 UI 配色细节、ECharts 主题调色板、表格列宽与分页默认条数（遵守 Pure Admin 亮/暗主题适配与离线零 CDN 红线）。
- `annotations.json` 的字段排版细节（需包含 `alarm_id`、`image_file`、`width`、`height`、`detections`、`roi`、`false_positive_reason`、`handle_note`）。
- 摄像头在线状态探活策略的内部轮询周期（复用 MediaMTX `/v3/paths/list` 同步更新 `cameras.status` 与 `last_online_at`）。
- 各里程碑内部具体的 `sqlc` 查询函数命名与组件拆分粒度。
