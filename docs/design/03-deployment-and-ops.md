# 部署架构、中间件配置与运维手册

本文档覆盖 **Docker Compose（含离线内网交付）** 与 **裸机部署（Linux/Windows）** 两套完整部署形态，以及 Nginx、MediaMTX 与多实例水平扩容配置规范。

---

## 1. 服务进程与端口规划（平台侧 & 算力机侧）

### 1.1 平台服务器（对外仅需暴露 Nginx 入口与 MediaMTX 媒体/信令端口）
| 组件 | 角色 | 默认内部端口 | 是否需对前端浏览器开放 |
| :--- | :--- | :--- | :---: |
| **`nginx`** | 前端静态页托管 + 统一反代 | `80` / `443` | ✅ 统一入口 |
| **`mediamtx`** | RTSP 中继 & WebRTC 媒体 | `8554` (RTSP)、`8889` (WHEP)、`8189` (UDP/TCP Mux)、`9997` (Control API 仅内网) | 仅 `8189` (WebRTC 媒体流)；WHEP 信令可经 Nginx `/whep/` 反代；`8554` 仅对算力机开放 |
| **`aegis-api`** | 控制面 REST API / RBAC / 鉴权 / 证据代理 | `8081` | ❌（经 Nginx `/api/` 反代） |
| **`aegis-ws`** | 浏览器 WebSocket 实时推送网关 | `8082` | ❌（经 Nginx `/ws/` 反代） |
| **`aegis-ingest`** | Agent 主动 gRPC 接入 + 规则引擎 | `9090` (gRPC) | 仅对算力机 `aegis-agent` 开放 |
| **`aegis-scheduler`** | 全局调度器与租约大脑 | `8083` (健康/监控) | ❌（纯内部进程） |
| **`aegis-dispatcher`** | 报警分发 + 短视频证据异步录制 | `8084` (健康/监控) | ❌（纯内部进程） |
| **`postgres`** | 主数据库 | `5432` | ❌（纯内部） |
| **`rabbitmq`** | 持久化消息队列 | `5672` (AMQP)、`15672` (管理台仅运维) | ❌（纯内部） |
| **`redis`** | 缓存 + Pub/Sub + 选主锁 | `6379` | ❌（纯内部） |
| **`minio`** | S3 兼容证据对象存储 | `9000` (S3 API)、`9001` (控制台仅运维) | ❌（证据经 Nginx→`aegis-api` 签名代理访问） |

### 1.2 计算主机（1 ~ N 台独立 GPU/算力服务器）
| 组件 | 角色 | 端口需求 |
| :--- | :--- | :--- |
| **`aegis-agent`** | 纯 Go 节点代理 | 主动出站连平台 `aegis-ingest:9090` (gRPC)，无需入站端口 |
| **Python Runtime (0~N)** | 按 `runtime_key` 由 Agent 启停的推理进程 | 仅监听本机 Unix Socket 或 `127.0.0.1` 临时端口；出站拉取 `mediamtx:8554` 授权 RTSP 流 |

---

## 2. Nginx 统一配置规范（前后端完全分离）

Nginx 承担四项核心职责：① 托管 `Vue Pure Admin` 构建产物 `dist/`；② 反代 `/api/` 至 `aegis-api` 集群；③ 反代 `/ws/` 至 `aegis-ws` 集群（透传 WebSocket Upgrade）；④ 反代 `/whep/` 至 MediaMTX（以及按需终结 HTTPS/WSS）。

关键配置约定：
```nginx
upstream aegis_api_pool {
    server aegis-api:8081;
    # 多实例直接追加 server 行
}

upstream aegis_ws_pool {
    server aegis-ws:8082;
}

server {
    listen 80;
    server_name _;

    root /usr/share/nginx/html;
    index index.html;

    # 前端 SPA 静态托管与路由回落
    location / {
        try_files $uri $uri/ /index.html;
    }

    # 静态资源长缓存
    location /assets/ {
        expires 30d;
        add_header Cache-Control "public, immutable";
    }

    # 控制面 REST API 与 S3 签名证据流式代理（支持 MP4 Range 拖拽）
    location /api/ {
        proxy_pass http://aegis_api_pool;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off; # 保证大图片/MP4 Range 流式零缓冲透传
    }

    # 浏览器实时 WebSocket 网关
    location /ws/ {
        proxy_pass http://aegis_ws_pool;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
    }

    # MediaMTX WebRTC WHEP 信令反代
    location /whep/ {
        proxy_pass http://mediamtx:8889/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```
> 注意：若内网启用 HTTPS/WSS，只需在此 `server` 块配内部 CA 证书开启 `listen 443 ssl`，后端 Go 进程、MediaMTX 与前端代码均零修改。

---

## 3. MediaMTX 关键配置规范

- **Control API**：开启 `api: yes`（仅绑定内网 `9997`），由 `aegis-scheduler` 自动同步摄像头路径（主码流 `cam_<id>`、可选子码流 `cam_<id>_sub`）。
- **低延迟 WebRTC**：
  - 开启 `webrtc: yes`、`webrtcAddress: :8889`
  - 单端口复用 `webrtcLocalUDPAddress: :8189` + TCP 兜底 `webrtcLocalTCPAddress: :8189`
  - 配置 `webrtcAdditionalHosts: [<平台内网固定IP>]`，确保跨 VLAN/NAT 内网浏览器稳定建连。
- **外部统一鉴权**：
  - 配置 `authMethod: http`、`authHTTPAddress: http://aegis-api:8081/internal/mediamtx/auth`
  - 区分前端 WebRTC 播放 Token（`stream:play:<token>`）与 Runtime 授权 RTSP 拉流 Token（`stream:pull:<token>`），未授权请求一律拒绝。
- **短期环形录像缓冲（支持报警前 5 秒短视频证据）**：
  - 对于开启 `record_video=true` 的摄像头，在 MediaMTX 对应路径启用极轻量的短期分段录像（如 `recordFormat: fmp4`、`recordSegmentDuration: 5s`、`recordDeleteAfter: 2m`，直接写内存盘/临时盘），使 `aegis-dispatcher` 在报警发生后可通过 `ffmpeg -c copy` 极速无损拼出“报警前 N 秒 + 报警后 M 秒”的 MP4 证据上传至 S3，CPU 开销接近于零。

---

## 4. Docker Compose 与离线内网交付

- **平台侧**：`deploy/docker/docker-compose.yml` 编排 11 个服务（`nginx`、`aegis-api`、`aegis-scheduler`、`aegis-ingest`、`aegis-ws`、`aegis-dispatcher`、`mediamtx`、`postgres`、`rabbitmq`、`redis`、`minio`），支持通过 `--scale aegis-api=2 --scale aegis-ws=2 --scale aegis-dispatcher=2` 一键横向扩容。
- **算力机侧**：`deploy/agent/docker-compose.yml` 独立部署 `aegis-agent`（并挂载本机 Python Runtime 环境与 NVIDIA GPU `--gpus all`）。
- **离线交付流程**：
  1. 在有网构建机编译前端 `dist/` 与 Go 镜像，执行 `docker save` 导出全部基础镜像与业务镜像为 `.tar` 压缩包；
  2. 拷贝至工厂内网服务器执行 `docker load -i ...` 导入；
  3. 修改 `.env`（内网 IP、数据库/RabbitMQ/MinIO/JWT 密钥）后执行 `docker compose up -d` 即可全离线运行。

---

## 5. 裸机部署指南（Linux systemd / Windows 服务）

针对现场禁用 Docker 的工业服务器/工控机：
1. **中间件安装**：按目标 OS 安装 PostgreSQL 16、RabbitMQ 3.13+、Redis 7+、MinIO、Nginx、MediaMTX 与 `ffmpeg`（平台侧按需）。
2. **前端部署**：将 `web/dist/` 拷至 Nginx 站点目录并应用标准 `nginx.conf` 模板。
3. **Go 服务部署**：
   - 执行统一构建脚本产出 6 个无依赖可执行文件：`aegis-api`、`aegis-scheduler`、`aegis-ingest`、`aegis-ws`、`aegis-dispatcher`（平台机），以及 `aegis-agent`（算力机）；
   - 先执行一次数据库迁移命令 `aegis-api migrate up` 初始化/升级 PostgreSQL 表结构；
   - 通过同一份 `config.yaml`（配合环境变量覆盖敏感密钥）分别以 `systemd` unit（Linux）或 Windows Service（如 NSSM / `sc`）托管启动，配置 `Restart=always` 自动拉起。

---

## 6. 多实例水平扩容与高可用运行手册

| 扩容对象 | 扩容方式 | 高可用与防脑裂要点 |
| :--- | :--- | :--- |
| `aegis-api` | 直接加实例并加入 Nginx `upstream` | 无状态；JWT 与流 Token 均在 Redis 共享 |
| `aegis-ws` | 直接加实例并加入 Nginx `upstream` | 无状态；全部实例订阅相同 Redis Pub/Sub 频道 |
| `aegis-dispatcher` | 直接加实例连同一 RabbitMQ | AMQP 竞争消费 + 手动 ACK，实例崩溃未确认消息自动重投 |
| `aegis-scheduler` | 部署 2 个实例（1 主 1 备） | 争抢 Redis 分布式锁 `scheduler:leader`（TTL 10s，每 3s 续约），主节点宕机 10s 内备节点自动接管 |
| `aegis-ingest` | 部署多实例 + gRPC L4 负载均衡（Nginx Stream / HAProxy） | Agent 连入任一实例后在 Redis 写入 `agent:route:<node_id> -> ingest_instance_id`，`aegis-scheduler` 经 RabbitMQ 定向发指令到该实例 |
| `aegis-agent` | 新增计算主机并部署 1 个 Agent | 主动连平台自动注册上线；整机掉线超过心跳阈值（15s）后，`aegis-scheduler` 自动将其承载任务迁移至其他具备对应 `runtime_key` 的健康主机 |
