# AegisVision 裸机部署指南（Linux systemd & Windows Service）

本目录提供不依赖 Docker 的工业现场裸机部署清单。全部 6 个 Go 进程均为 `CGO_ENABLED=0` 零依赖静态二进制，可直接交叉编译后拷贝至目标内网主机运行。

---

## 1. 编译纯 Go 二进制与前端静态产物

在有网构建机执行：

```bash
# 1) 编译后端 6 个 Linux amd64 纯 Go 二进制
cd backend
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build
# （Windows 目标使用 GOOS=windows GOARCH=amd64）

# 2) 编译前端离线静态产物（零外部 CDN）
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
# 产物目录：web/dist/
```

---

## 2. Linux (systemd) 平台服务器部署

### 2.1 安装基础中间件
在平台服务器安装并启动：
- PostgreSQL 16（创建数据库 `aegisvision` 与用户 `aegis`）
- Redis 7（启用 `--appendonly yes` 与 `--requirepass`）
- RabbitMQ 3.13+
- MinIO（创建私有桶 `aegis-evidence`）
- MediaMTX v1.12+（使用 `deploy/mediamtx/mediamtx.yml`）
- Nginx（使用 `deploy/nginx/nginx.conf`，将 `web/dist/` 拷至 `/usr/share/nginx/html`）
- `ffmpeg`（可选，仅 `aegis-dispatcher` 录制短视频 MP4 证据时需要）

### 2.2 部署二进制与配置
```bash
sudo useradd -r -s /sbin/nologin aegis || true
sudo mkdir -p /opt/aegisvision/bin /etc/aegisvision /var/lib/aegisvision/evidence
sudo cp backend/bin/aegis-{api,scheduler,ingest,ws,dispatcher} /opt/aegisvision/bin/
sudo cp deploy/baremetal/config.yaml.example /etc/aegisvision/config.yaml
# 按现场环境编辑 /etc/aegisvision/config.yaml，并设置最小权限
sudo chown -R aegis:aegis /opt/aegisvision /var/lib/aegisvision
sudo chmod 640 /etc/aegisvision/config.yaml
```

### 2.3 初始化数据库迁移
```bash
sudo -u aegis /opt/aegisvision/bin/aegis-api migrate up -config /etc/aegisvision/config.yaml
```

### 2.4 安装并启动 systemd 服务
```bash
sudo cp deploy/baremetal/systemd/aegis-{api,scheduler,ingest,ws,dispatcher}.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now aegis-api aegis-scheduler aegis-ingest aegis-ws aegis-dispatcher
sudo systemctl status aegis-api aegis-scheduler aegis-ingest aegis-ws aegis-dispatcher
```

---

## 3. Linux 算力主机部署 `aegis-agent`

```bash
sudo mkdir -p /opt/aegisvision-agent/bin /opt/aegisvision-agent/data /etc/aegisvision
sudo cp backend/bin/aegis-agent /opt/aegisvision-agent/bin/
sudo cp deploy/agent/agent.yaml.example /etc/aegisvision/agent.yaml
# 修改 agent.yaml 中的 node_id、ingest_addr、agent_token 与本地 Python Runtime 映射
sudo cp deploy/baremetal/systemd/aegis-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now aegis-agent
```

---

## 4. Windows Server / 工控机部署

1. 将编译好的 `*.exe` 放入 `C:\AegisVision\bin\`，将 `config.yaml`（或 `agent.yaml`）放入 `C:\AegisVision\`。
2. 下载 [NSSM](https://nssm.cc/) 并将 `nssm.exe` 放入系统 `PATH`。
3. 以管理员身份运行 PowerShell：

```powershell
# 平台机（自动执行数据库迁移并注册 5 个服务）
.\deploy\baremetal\windows\install-services.ps1 -Role platform -InstallDir "C:\AegisVision"

# 算力机
.\deploy\baremetal\windows\install-services.ps1 -Role agent -InstallDir "C:\AegisVision"
```

卸载服务：
```powershell
.\deploy\baremetal\windows\install-services.ps1 -Role platform -Uninstall
```
