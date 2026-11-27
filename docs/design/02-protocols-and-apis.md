# 通信协议与接口规范

本文档锁定平台各子系统之间的完整契约，包含：
1. 计算主机 `aegis-agent` ↔ 平台 `aegis-ingest`（gRPC `agent.proto`）
2. 计算主机 `aegis-agent` ↔ 本机算法 `Runtime`（gRPC `runtime.proto`）
3. Web 前端 ↔ `aegis-api`（REST API 与 RBAC 权限矩阵）
4. Web 前端 ↔ `aegis-ws`（WebSocket 实时协议）
5. RabbitMQ 交换机/队列/DLX 拓扑 与 Redis Key/PubSub 规范
6. 外部 Webhook 负载与 HMAC-SHA256 签名规范

---

## 1. 通用空间坐标约定
所有检测框与 ROI 多边形统一使用**相对原图宽高的归一化坐标 `[0.0, 1.0]`**：
- 点：`{ "x": 0.25, "y": 0.40 }`（左上角为 `(0, 0)`，右下角为 `(1, 1)`）
- 矩形框：`[x1, y1, x2, y2]`（满足 `0 <= x1 < x2 <= 1` 且 `0 <= y1 < y2 <= 1`）

---

## 2. `aegis-agent` ↔ 平台 `aegis-ingest`（gRPC 协议 `agent.proto`）

由 `aegis-agent` **主动向 `aegis-ingest` 发起出站 gRPC 连接**（元数据携带 `x-agent-token` 集群认证密钥与 `x-node-id`）。

### 2.1 RPC 服务定义概要
```protobuf
syntax = "proto3";
package aegis.agent.v1;

service AgentGateway {
  // 1. 双向控制流：Agent 注册、心跳、上报 Runtime/任务状态；平台下发任务启停与租约续期
  rpc ControlStream(stream AgentUpstream) returns (stream PlatformDownstream);

  // 2. 高频实时帧流（非持久化、内存直通、拥塞丢旧保新）：用于实时画框与离岗心跳
  rpc StreamLiveFrames(stream LiveFrameBatch) returns (StreamAck);

  // 3. 异常事件与抓拍图可靠上报（Agent 本地磁盘缓存断点续传，带 event_id 幂等确认）
  rpc ReportBufferedEvents(stream BufferedEvent) returns (stream EventAck);
}
```

### 2.2 核心消息字段约束
- **`AgentUpstream`（oneof）**：
  - `RegisterRequest`：`node_id`、`hostname`、`ip_addr`、`runtime_keys[]`、`max_tasks`、`agent_version`
  - `Heartbeat`：`node_id`、`timestamp_ms`、`running_tasks[]`（含 `task_id`、`runtime_key`、`actual_state`、`error_message`）、`host_metrics`（CPU/内存/GPU 使用率）
  - `TaskAck`：`command_id`、`task_id`、`accepted`、`error_message`
- **`PlatformDownstream`（oneof）**：
  - `AssignTaskCommand`：`command_id`、`task_id`、`camera_id`、`algorithm_version_id`、`runtime_key`、`stream_url`（默认授权内部 RTSP，带短期 Token）、`stream_protocol`（`rtsp` / `hls`）、`rois[]`、`algo_params_json`、`lease_id`、`lease_ttl_seconds`、`config_version`
  - `UpdateTaskCommand`：`command_id`、`task_id`、`rois[]`、`algo_params_json`、`config_version`
  - `RevokeTaskCommand`：`command_id`、`task_id`、`reason`
  - `RenewLeaseCommand`：`task_id`、`lease_id`、`lease_ttl_seconds`、`refreshed_stream_url`（Token 即将过期前平滑刷新授权流地址）
- **`LiveFrameBatch` / `DetectionFrame`**：
  - `task_id`、`camera_id`、`algorithm_version_id`、`timestamp_ms`、`frame_width`、`frame_height`、`is_heartbeat`（无目标空帧心跳标记）、`detections[]`（`bbox[4]`、`label`、`confidence`、`track_id`、`is_abnormal`、`attributes`）、`snapshot_jpeg`（`bytes`，仅异常触发帧携带）
- **`BufferedEvent` & `EventAck`（断网续传）**：
  - `event_id`（UUID，用于平台 `alarms.event_dedup_key` 幂等去重）、`frame`（含 `snapshot_jpeg`）、`cached_at_ms`；
  - Agent 在 `StreamLiveFrames` 正常在线时直接随帧上报异常，当与平台断连时自动把异常帧与抓拍 `bytes` 写入本地 SQLite/WAL 队列；重连后通过 `ReportBufferedEvents` 逐批上报，收到对应 `EventAck(event_id)` 后才从本地磁盘删除。

---

## 3. `aegis-agent` ↔ 本机 `Runtime`（gRPC 协议 `runtime.proto`）

算法工程师使用 Python（PyTorch / ONNX Runtime / TensorRT 等）实现本协议，监听 `aegis-agent` 分配的本机 Unix Domain Socket（Linux）或回环端口 `127.0.0.1:<port>`（Windows/跨平台）。

### 3.1 RPC 服务定义概要
```protobuf
syntax = "proto3";
package aegis.runtime.v1;

service AlgorithmRuntime {
  rpc Health(HealthRequest) returns (HealthResponse);
  rpc AssignTask(RuntimeAssignTaskRequest) returns (RuntimeTaskReply);
  rpc UpdateTask(RuntimeUpdateTaskRequest) returns (RuntimeTaskReply);
  rpc RevokeTask(RuntimeRevokeTaskRequest) returns (RuntimeTaskReply);
  rpc RenewLease(RuntimeRenewLeaseRequest) returns (RuntimeTaskReply);

  // Runtime 持续推送本进程内所有活跃任务的推理结果与心跳
  rpc StreamResults(StreamResultsRequest) returns (stream RuntimeResult);
}
```

### 3.2 Agent 与 Runtime 的核心契约约定
1. **按 `runtime_key` 进程隔离与多任务复用**：
   - `aegis-agent` 本地配置文件维护 `runtime_key -> 启动命令/工作目录/环境变量/最大任务数` 映射；
   - 当首个属于 `runtime_key` 的任务到达时，`aegis-agent` 自动拉起对应 Runtime 进程，等待 `Health()` 就绪后调用 `AssignTask()` 并建立 `StreamResults()`；
   - 后续同 `runtime_key` 的任务直接发往同一个已运行的 Runtime 进程（是否复用同一路 RTSP 拉流解码由 Runtime 内部实现）；
   - 当该 Runtime 下活跃任务数归零超过空闲宽限期（默认 60s），`aegis-agent` 发送停止信号优雅回收进程以释放显存。
2. **租约（Lease）自停止保障**：
   - 每个任务带有 `lease_id` 和 `lease_ttl_seconds`（默认 30s）；`aegis-agent` 每 `TTL / 3`（10s）续约一次；
   - 若 Runtime 超过 `lease_ttl_seconds` 未收到 `RenewLease`，**必须主动停止该任务的拉流与推理并释放资源**，杜绝 Agent 异常退出后的僵尸进程。
3. **空帧心跳要求**：
   - Runtime 运行期间即便当前画面无任何目标，也必须按约定间隔（默认 1 秒）上报 `is_heartbeat = true, detections = []` 的 `RuntimeResult`，供平台安全推进离岗检测计时。
4. **抓拍图附带**：
   - 当检测结果包含异常目标（或满足参数中声明的抓拍标签）时，Runtime 将当前原始帧编码为 JPEG 写入 `snapshot_jpeg`（建议质量 80~85，长边不超过 1920px），确保抓拍图与检测框 100% 同一帧。

---

## 4. REST API 规范与 RBAC 权限矩阵（`aegis-api`）

统一前缀：`/api/v1`，响应格式：
```json
{ "code": 0, "message": "ok", "data": {} }
```

### 4.1 角色权限矩阵
| 能力域 | `admin` | `operator` | `viewer` |
| :--- | :---: | :---: | :---: |
| 登录 / 刷新 Token / 修改本人密码 | ✅ | ✅ | ✅ |
| 实时监控页 / 申请 WebRTC 短期流 Token / 订阅 WS | ✅ | ✅ | ✅ |
| 报警列表查询 / 详情查看 / 图片与 MP4 证据查看 | ✅ | ✅ | ✅ |
| 报警处理（确认 / 误报 / 备注）& 手动重发失败投递 | ✅ | ✅ | ❌ |
| 用户与角色管理（增删改、重置密码） | ✅ | ❌ | ❌ |
| 算法目录与版本管理 | ✅ | ❌ | ❌ |
| 摄像头、ROI 区域与区域算法规则配置 | ✅ | ❌ | ❌ |
| 算力节点查看与调度操作 / 报警通道管理 | ✅ | ❌ | ❌ |

### 4.2 核心端点清单
- **认证与用户**：
  - `POST /api/v1/auth/login`、`POST /api/v1/auth/refresh`、`POST /api/v1/auth/logout`、`GET /api/v1/auth/me`
  - `GET/POST /api/v1/users`、`PUT/DELETE /api/v1/users/:id`、`POST /api/v1/users/:id/reset-password`
- **算法与版本**：
  - `GET/POST /api/v1/algorithms`、`PUT/DELETE /api/v1/algorithms/:id`
  - `GET/POST /api/v1/algorithms/:id/versions`、`PUT /api/v1/algorithm-versions/:version_id`（发布/弃用/修改默认参数）
- **摄像头、流 Token、ROI 与规则**：
  - `GET/POST /api/v1/cameras`、`GET/PUT/DELETE /api/v1/cameras/:id`
  - `POST /api/v1/cameras/:id/stream-token`（签发短期播放 Token，写 Redis TTL 60s，同时返回 WebRTC `whep_url` 与 LL-HLS `hls_url` 供前端双模式预览，复用 `/internal/mediamtx/auth` 对 `webrtc` / `hls` 协议鉴权）
  - `GET/POST /api/v1/cameras/:id/rois`、`PUT/DELETE /api/v1/rois/:roi_id`
  - `GET/POST /api/v1/cameras/:id/rules`、`PUT/DELETE /api/v1/rules/:rule_id`
- **算力节点与任务监控**：
  - `GET /api/v1/nodes`、`GET /api/v1/nodes/:node_id`、`PUT /api/v1/nodes/:node_id/status`（维护排水 `draining`）
  - `GET /api/v1/tasks`、`POST /api/v1/tasks/:id/reschedule`
- **报警中心与证据访问**：
  - `GET /api/v1/alarms`（支持时间、摄像头、规则模式、处理状态筛选与分页）、`GET /api/v1/alarms/:id`
  - `POST /api/v1/alarms/:id/handle`（确认/误报/备注）
  - `GET /api/v1/alarms/:id/deliveries`、`POST /api/v1/deliveries/:delivery_id/retry`
  - `GET /api/v1/evidence/:alarm_id/image`、`GET /api/v1/evidence/:alarm_id/video`（支持 JWT 或 `?exp=&sig=` 签名校验，视频支持 Range 请求）
- **报警通道**：
  - `GET/POST /api/v1/channels`、`PUT/DELETE /api/v1/channels/:id`、`POST /api/v1/channels/:id/test`
- **内部鉴权回调（仅内网 MediaMTX 访问，Nginx 不对外暴露）**：
  - `POST /internal/mediamtx/auth`（校验前端 WebRTC 播放 Token 或 Runtime 授权拉流 Token）

---

## 5. 浏览器 WebSocket 协议（`aegis-ws`）

- 连接地址：`ws(s)://<host>/ws/realtime?token=<jwt_access_token>`（或首帧鉴权）
- **客户端上行消息**：
  ```json
  { "type": "subscribe", "camera_ids": ["cam_uuid_1", "cam_uuid_2"] }
  ```
- **服务端下行消息**：
  1. **实时检测框与区域人数**（高频，慢连接丢旧保新）：
     ```json
     {
       "type": "frame_detections",
       "camera_id": "cam_uuid_1",
       "algorithm_key": "person_detect",
       "timestamp_ms": 1795700000123,
       "detections": [
         { "bbox": [0.12, 0.25, 0.30, 0.78], "label": "person", "confidence": 0.91, "is_abnormal": false, "track_id": "12" }
       ],
       "roi_counts": { "roi_uuid_1": 3 }
     }
     ```
  2. **实时报警提醒**：
     ```json
     {
       "type": "alarm_alert",
       "alarm_id": "alarm_uuid",
       "camera_id": "cam_uuid_1",
       "camera_name": "1号冲压车间",
       "rule_mode": "absence_detect",
       "title": "1号工位离岗超过60秒",
       "severity": "critical",
       "triggered_at": "2026-11-26T14:20:00Z",
       "image_url": "/api/v1/evidence/alarm_uuid/image"
     }
     ```

---

## 6. RabbitMQ 拓扑与 Redis Key / PubSub 规范

### 6.1 RabbitMQ 交换机与队列
| 交换机 | 类型 | 队列 / 路由键 | 消费者 | 持久化与重试策略 |
| :--- | :--- | :--- | :--- | :--- |
| `aegis.alarms` | `direct` | `alarms.triggered` | `aegis-dispatcher`（竞争消费） | 持久化 + 手动 ACK；失败路由至 DLX 延迟队列 `alarms.retry.3s` → `15s` → `60s`，超限进 `alarms.dlq` |
| `aegis.evidence` | `direct` | `evidence.record` | `aegis-dispatcher`（竞争消费） | 持久化 + 手动 ACK；录制短视频上传 S3，失败重试 2 次后标记 `video_status=failed` |
| `aegis.control` | `topic` | `agent.cmd.<ingest_instance_id>` | 对应 `aegis-ingest` 实例 | 临时独占队列（随实例启动绑定），用于 `aegis-scheduler` 精准投递任务/租约命令给持有目标 Agent 连接的实例 |
| `aegis.config` | `fanout` | 各进程匿名队列 | `aegis-scheduler` / `aegis-ingest` | `aegis-api` 修改摄像头/ROI/规则/算法版本后广播热更新事件 |

### 6.2 Redis Key 与 Pub/Sub 规范
- **Pub/Sub 频道**：
  - `frames:<camera_id>`：高频实时检测框与区域人数（`aegis-ingest` 发布 → `aegis-ws` 订阅）
  - `alarms:realtime`：全局实时报警提醒（`aegis-ingest` 发布 → `aegis-ws` 订阅）
- **热状态 Key**：
  - `scheduler:leader`（String + TTL 10s，每 3s 续约）：`aegis-scheduler` 主备选主锁
  - `agent:route:<node_id>`（String + TTL 30s）：记录当前持有该 Agent 长连接的 `ingest_instance_id`
  - `stream:play:<token>`（JSON + TTL 60s）：前端 WebRTC 播放授权 Token（含 `user_id`、`stream_path`）
  - `stream:pull:<token>`（JSON + TTL 等于任务租约 + 30s）：Runtime 授权内部 RTSP 拉流 Token（含 `task_id`、`stream_path`）
  - `auth:blacklist:<jti>`（String + 剩余过期时间）：已登出 Access Token 黑名单

---

## 7. 外部 Webhook 负载与签名规范

- **请求头**：
  - `Content-Type: application/json`
  - `X-Aegis-Event: alarm.triggered`
  - `X-Aegis-Delivery-Id: <delivery_uuid>`
  - `X-Aegis-Timestamp: <unix_seconds>`
  - `X-Aegis-Signature: sha256=<hex(HMAC_SHA256(secret, timestamp + "." + raw_body))>`（配置 `secret` 时附带）
- **请求体示例**：
```json
{
  "event": "alarm.triggered",
  "alarm_id": "01936a1b-...",
  "triggered_at": "2026-11-26T14:20:00Z",
  "severity": "critical",
  "rule_mode": "absence_detect",
  "title": "1号工位离岗超过60秒",
  "camera": { "id": "cam_uuid", "name": "1号冲压车间", "location": "A区东侧" },
  "roi": { "id": "roi_uuid", "name": "1号值班工位" },
  "algorithm": { "key": "person_detect", "version": "v1.2.0" },
  "detected_count": 0,
  "detections": [],
  "evidence": {
    "image_url": "http://192.168.1.100/api/v1/evidence/01936a1b-.../image?exp=1795786400&sig=...",
    "video_status": "recording",
    "video_url": "http://192.168.1.100/api/v1/evidence/01936a1b-.../video?exp=1795786400&sig=..."
  }
}
```
