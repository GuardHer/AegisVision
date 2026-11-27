# AegisVision Enterprise Redesign — Milestone 3 (M3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Milestone 3 (M3) — the final enterprise governance & closed-loop operations milestone: multi-channel industrial notifications (`wecom`, `dingtalk`, `sound_light` with auto/handle/manual reset + explicit `auto_route` tree routing), false-positive sample dataset & training ZIP export (`/datasets`), workshop-level data-scope RBAC across REST & real-time WebSocket with instant online revocation, audit logs (`/audit-logs`), global settings & health (`/settings`), and zero-CGO compute-host CPU/Memory/GPU metrics.

**Architecture:** Preserves the 6-process pure-Go (`CGO_ENABLED=0`) architecture and strict package isolation (`api`, `scheduler`, `ingest`, `ws`, `notifier`/`evidence`, `agent` never import one another). Manual/handle-triggered sound-light resets travel via RabbitMQ (`alarms.triggered` control payload) from `aegis-api` to `aegis-dispatcher`; workshop data-scope changes invalidate Redis cache and broadcast `auth:scope_changed` to `aegis-ws` for instant live revocation; all database tables (`user_zones`, `audit_logs`, `system_settings`, `alarm_channels.routing_rules`, `users.all_zones_access`) already exist from migration `00002_enterprise_features.sql`.

**Tech Stack:** Go 1.24 (`CGO_ENABLED=0`, Gin, `pgx/v5`, `sqlc`, `buf`, `golang.org/x/sys/windows` / Linux `/proc`, external `nvidia-smi` CLI), PostgreSQL 16, Redis Pub/Sub, RabbitMQ, Vue 3 + TypeScript + Element Plus + Vitest (zero external CDN).

**Spec:** [`docs/superpowers/specs/2026-11-27-enterprise-platform-redesign.md`](../specs/2026-11-27-enterprise-platform-redesign.md)

## Global Constraints

- Pure Go zero CGO (`CGO_ENABLED=0`); host CPU/RAM metrics use `/proc` on Linux and Win32 syscalls on Windows; GPU metrics use external `nvidia-smi` CLI (1s timeout) with graceful `has_gpu=false` degradation.
- All `.proto` changes MUST pass `buf lint`, `buf breaking`, and `buf generate` (never hand-edit `gen/go/`).
- `internal/api` MUST NOT import `internal/notifier`; channel reset commands pass through RabbitMQ to `aegis-dispatcher`.
- `alarm_channels.routing_rules` MUST only auto-route when `"auto_route": true` is explicitly set (default `false` preserves existing explicit rule bindings).
- Audit logs MUST redact secrets/passwords/tokens and NEVER block primary write requests if audit insertion fails.
- Zero external CDN assets in `web/dist/`; all new UI copy synced in both `web/locales/zh-CN.yaml` and `web/locales/en.yaml`.

## Review Focus

1. **Workshop RBAC leak on WebSocket & Evidence URLs**: A restricted workshop operator/viewer MUST NOT receive `frame_detections` or `alarm_alert` WS events from unauthorized workshops, MUST receive `403` when requesting unauthorized camera/alarm/evidence/dataset endpoints, and MUST be evicted from unauthorized WS camera subscriptions immediately when an admin revokes workshop access (pinned in Task 3 tests).
2. **Explicit `auto_route` guard**: A channel with `routing_rules: {"auto_route": false}` (or empty) MUST NOT auto-receive alarms from unrelated rules; a channel with `auto_route: true` and a parent factory/workshop `zone_id` MUST match alarms triggered in descendant workstations and deduplicate against explicit `rule_alarm_channels` bindings (pinned in Task 1 tests).
3. **Sound-light reset safety & DingTalk HMAC signing**: `sound_light` manual reset and alarm-handle reset MUST work via RabbitMQ without importing `notifier` into `api`; `auto_reset_seconds` timer MUST cancel cleanly on context shutdown; DingTalk signature MUST match `base64(hmac_sha256(secret, timestamp + "\n" + secret))` URL-encoded (pinned in Task 1 tests).
4. **Audit log secret redaction & non-blocking failure**: Audit records for user creation/password reset and channel creation MUST strip `password`, `new_password`, `secret`, and `token` fields from `detail` JSONB (pinned in Task 4 tests).
5. **GPU-less host metric degradation**: On compute hosts without `nvidia-smi` (or non-GPU hosts), `CollectHostMetrics` MUST succeed with `has_gpu: false` (distinct from `0%` GPU usage) and valid CPU/Memory percentages (pinned in Task 5 tests).

---

### Task 1: Multi-Channel Notifiers (`wecom`, `dingtalk`, `sound_light`), Explicit Auto-Routing, & Channel Reset

**Files:**
- Create: `backend/internal/notifier/wecom.go`
- Create: `backend/internal/notifier/dingtalk.go`
- Create: `backend/internal/notifier/sound_light.go`
- Create: `backend/internal/notifier/channels_test.go`
- Modify: `backend/internal/notifier/dispatcher.go`
- Modify: `backend/internal/notifier/postgres_store.go`
- Modify: `backend/cmd/aegis-dispatcher/main.go`
- Modify: `backend/internal/platform/postgres/queries/rules.sql`
- Modify: `backend/internal/ingest/postgres_repo.go`
- Modify: `backend/internal/api/handlers_channels.go`
- Modify: `backend/internal/api/handlers_alarms.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Produces (`notifier`):
  - `NewWeComNotifier(client *http.Client) *WeComNotifier` (`Type() == "wecom"`)
  - `NewDingTalkNotifier(client *http.Client) *DingTalkNotifier` (`Type() == "dingtalk"`)
  - `NewSoundLightNotifier(client *http.Client) *SoundLightNotifier` (`Type() == "sound_light"`, plus `Reset(ctx, rawConfig) (SendResult, error)`)
  - `Dispatcher.HandleMessage` supports `{"action":"reset_channel","channel_id":"..."}` and schedules bounded `auto_reset_seconds` reset after successful `sound_light` trigger.
- Produces (`api`):
  - `POST /api/v1/channels` & `PUT /api/v1/channels/:id` accept `channel_type` in `webhook|wecom|dingtalk|sound_light` and `routing_rules` JSONB; `POST /api/v1/channels/:id/test` supports all 4 types; `POST /api/v1/channels/:id/reset` (admin/operator) enqueues `reset_channel` to RabbitMQ.
  - `AlarmsHandler.Handle` / `BatchHandle` enqueues `reset_channel` for any `sound_light` channels linked to handled alarms.

- [ ] **Step 1: Write failing unit tests in `backend/internal/notifier/channels_test.go`**
  Test WeCom Markdown payload, DingTalk HMAC-SHA256 query signing (Review Focus #3), `sound_light` trigger + template rendering + manual `reset_channel` + `auto_reset_seconds` delayed reset via `httptest.Server`, and explicit `auto_route` zone-tree matching + deduplication (Review Focus #2).
- [ ] **Step 2: Implement `wecom.go`, `dingtalk.go`, `sound_light.go`, `dispatcher.go` reset & auto-reset, and register in `cmd/aegis-dispatcher/main.go`**
- [ ] **Step 3: Update `rules.sql` (`routing_rules`), `ingest/postgres_repo.go` (auto-route + dedup), `handlers_channels.go`, `handlers_alarms.go`, and `router.go`**
- [ ] **Step 4: Run `sqlc generate`, lint, tests, and commit `feat(notifier): add wecom, dingtalk, sound_light plugins, reset control, and explicit auto_route`**

---

### Task 2: False-Positive Sample Library & Offline Training Package ZIP Export (`/api/v1/datasets/*`)

**Files:**
- Create: `backend/internal/platform/postgres/queries/datasets.sql`
- Create: `backend/internal/api/handlers_datasets.go`
- Create: `backend/internal/api/handlers_datasets_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Produces (`api`):
  - `GET /api/v1/datasets/false-positives?zone_id=&algorithm_version_id=&false_positive_reason=&start_time=&end_time=&page=&page_size=` (admin, operator)
  - `POST /api/v1/datasets/export` (admin, operator) -> streaming ZIP (`images/<alarm_id>.jpg` + `annotations.json` with `alarm_id`, `image_file`, `camera_name`, `zone_name`, `detections`, `roi`, `false_positive_reason`, `handle_note`, capped via `system_settings` default 500 / hard cap 2000).

- [ ] **Step 1: Add `datasets.sql` and run `sqlc generate`**
- [ ] **Step 2: Write failing integration test in `backend/internal/api/handlers_datasets_test.go`**
  Seed confirmed + false-positive (with & without snapshot) alarms; verify listing returns only snapshot-bearing `false_positive` items, viewer receives `403`, and ZIP export contains valid JPEG + `annotations.json`.
- [ ] **Step 3: Implement `handlers_datasets.go`, mount routes in `router.go`, verify tests pass, and commit `feat(datasets): add false-positive sample query and streaming training zip export`**

---

### Task 3: Workshop Data-Scope RBAC (`internal/api/scope.go` & `aegis-ws` Live Filtering + Revocation)

**Files:**
- Create: `backend/internal/platform/postgres/queries/user_zones.sql`
- Create: `backend/internal/api/scope.go`
- Create: `backend/internal/api/handlers_scope_test.go`
- Modify: `backend/internal/platform/redis/redis.go`
- Modify: `backend/internal/ingest/processor.go` & `postgres_repo.go` (`wsAlarmAlertMessage.ZoneID`)
- Modify: `backend/internal/ws/hub.go`, `server.go`, `hub_test.go`
- Modify: `backend/cmd/aegis-ws/main.go`
- Modify: `backend/internal/api/handlers_users.go`, `handlers_zones.go`, `handlers_cameras.go`, `handlers_alarms.go`, `handlers_dashboard.go`, `handlers_analytics.go`, `handlers_datasets.go`, `handlers_evidence.go`, `router.go`

**Interfaces:**
- Produces (`api`):
  - `type UserZoneScope struct { Unrestricted bool; AllowedZoneIDs []uuid.UUID; AllowedZoneSet map[uuid.UUID]struct{} }`
  - `func ResolveUserScope(ctx context.Context, store *postgres.Store, claims auth.Claims) (UserZoneScope, error)`
  - `PUT /api/v1/users/:id/zones` (`{"all_zones_access": false, "zone_ids": [...]}`) & `GET /api/v1/users/:id/zones` (admin only; invalidates Redis cache & publishes `auth:scope_changed`).
- Produces (`ws`):
  - `ClientScope { UserID string; Unrestricted bool; AllowedZones map[string]struct{}; AllowedCameras map[string]struct{} }`
  - `Hub.RegisterWithScope(clientID string, scope ClientScope) *Client`, `Hub.UpdateUserScope(userID string, scope ClientScope)` (immediately drops unauthorized camera subscriptions!).
  - `Hub.BroadcastAlarm` filters by `zone_id` / `camera_id` for restricted clients.

- [ ] **Step 1: Add `user_zones.sql`, run `sqlc generate`, add Redis scope cache + `auth:scope_changed` Pub/Sub helpers in `platform/redis/redis.go`**
- [ ] **Step 2: Write failing tests in `handlers_scope_test.go` and `ws/hub_test.go` (Review Focus #1)**
  Verify restricted workshop operator sees ONLY authorized workshop zones/cameras/alarms/analytics/datasets, gets `403` on other workshop camera/alarm/evidence, and `ws.Hub` blocks unauthorized frames & alarms and immediately revokes live subscriptions on `UpdateUserScope`.
- [ ] **Step 3: Implement `scope.go`, wire across REST handlers & `users/:id/zones`, add `zone_id` to `wsAlarmAlertMessage`, upgrade `ws.Hub` / `ws.Server` / `cmd/aegis-ws/main.go`**
- [ ] **Step 4: Run lint & tests across `api`, `ws`, `ingest`, `platform/redis`, and commit `feat(rbac): enforce workshop zone data-scope across rest apis and realtime ws with online revocation`**

---

### Task 4: Audit Logs, Global System Settings, Platform Health, & Algorithm 30-Day Stats

**Files:**
- Create: `backend/internal/platform/postgres/queries/governance.sql`
- Create: `backend/internal/api/audit.go`
- Create: `backend/internal/api/handlers_governance.go`
- Create: `backend/internal/api/handlers_governance_test.go`
- Modify: `backend/internal/api/handlers_algorithms.go`, `handlers_users.go`, `handlers_zones.go`, `handlers_cameras.go`, `handlers_rules.go`, `handlers_schedules.go`, `handlers_channels.go`, `handlers_alarms.go`, `router.go`

**Interfaces:**
- Produces (`api`):
  - `recordAudit(c *gin.Context, store *postgres.Store, log *slog.Logger, action, resourceType, resourceID, summary string, detail map[string]any)` (redacts `password`/`new_password`/`secret`/`token` keys; non-blocking on error).
  - `GET /api/v1/audit-logs` (admin only, paginated + filtered).
  - `GET /api/v1/settings` (all roles), `PUT /api/v1/settings` (admin only).
  - `GET /api/v1/system/health` (admin only — Postgres, Redis, MediaMTX, Storage, online nodes/cameras summary).
  - `GET /api/v1/algorithms/:id/versions` enriches each version with 30-day `total_alarms`, `confirmed_count`, `false_positive_count`, `false_positive_rate`.

- [ ] **Step 1: Add `governance.sql` and run `sqlc generate`**
- [ ] **Step 2: Write failing integration tests in `handlers_governance_test.go` (Review Focus #4)**
  Verify write actions produce audit rows with redacted secrets, `GET /api/v1/settings` & `PUT /api/v1/settings` persist values, `GET /api/v1/system/health` reports component statuses, and algorithm versions include 30-day FP stats.
- [ ] **Step 3: Implement `audit.go`, `handlers_governance.go`, enrich `handlers_algorithms.go`, wire `recordAudit` on write endpoints and routes in `router.go`**
- [ ] **Step 4: Run tests & commit `feat(governance): add audit logs with secret redaction, system settings, health panel, and algo 30d stats`**

---

### Task 5: Zero-CGO Compute Host CPU/Memory/GPU Metrics (`agent.proto` + `internal/agent/metrics*.go`)

**Files:**
- Modify: `backend/api/proto/agent/v1/agent.proto`
- Create: `backend/internal/agent/metrics.go`
- Create: `backend/internal/agent/metrics_linux.go`
- Create: `backend/internal/agent/metrics_windows.go`
- Create: `backend/internal/agent/metrics_other.go`
- Create: `backend/internal/agent/metrics_test.go`
- Modify: `backend/internal/agent/agent.go`
- Modify: `backend/internal/ingest/gateway.go`

**Interfaces:**
- Produces (`agent.proto`): `HostMetrics` adds `bool has_gpu = 5;` and `float gpu_temp_c = 6;` (backwards-compatible proto3 extension).
- Produces (`agent`): `CollectHostMetrics(ctx context.Context) *agentv1.HostMetrics` and `ParseNvidiaSMIOutput(raw string) (gpuPct, memPct, tempC float32, ok bool)`.

- [ ] **Step 1: Extend `agent.proto` and regenerate via `buf lint && buf generate`**
- [ ] **Step 2: Write failing unit tests in `backend/internal/agent/metrics_test.go` (Review Focus #5)**
  Verify `ParseNvidiaSMIOutput` parses valid CSV (`72, 4096, 16384, 65` -> `72%`, `25%`, `65°C`, `ok=true`), handles multi-GPU averaging, and `CollectHostMetrics` succeeds with `has_gpu=false` when `nvidia-smi` binary is absent.
- [ ] **Step 3: Implement OS-specific zero-CGO CPU/RAM collectors + `nvidia-smi` 1s-timeout probe, wire into `NodeAgent` heartbeat and `ingest/gateway.go` (`agent_nodes.meta` JSON storage)**
- [ ] **Step 4: Run `buf lint`, `golangci-lint`, `go test ./internal/agent/... ./internal/ingest/...`, and commit `feat(agent): add zero-cgo host cpu/ram/gpu metrics collection and proto fields`**

---

### Task 6: Frontend False-Positive Datasets (`/datasets`), Upgraded Algorithms (`/algorithms`), & Compute Cluster (`/nodes`)

**Files:**
- Modify: `web/locales/zh-CN.yaml` & `web/locales/en.yaml`
- Modify: `web/src/api/aegis.ts`
- Modify: `web/src/router/modules/aegis.ts`
- Create: `web/src/views/datasets/index.vue`
- Create: `web/src/views/datasets/__tests__/datasets.spec.ts`
- Modify: `web/src/views/algorithms/index.vue`
- Modify: `web/src/views/nodes/index.vue`

- [ ] **Step 1: Add all M3 i18n keys (`zh-CN.yaml`, `en.yaml`), TypeScript types & API clients (`aegis.ts`), and sidebar routes (`/datasets`, `/audit-logs`, `/settings` in `router/modules/aegis.ts`)**
- [ ] **Step 2: Build `web/src/views/datasets/index.vue` + unit test `datasets.spec.ts`**
  Filterable card preview grid of false-positive samples with 1-click streaming training package ZIP download (`exportFalsePositiveDatasetBlob`).
- [ ] **Step 3: Upgrade `algorithms/index.vue` (30-day hits, confirmed, FP rate badges) and `nodes/index.vue` (CPU / Memory / GPU / VRAM progress bars with graceful "No GPU metrics" fallback when `has_gpu === false`)**
- [ ] **Step 4: Verify `typecheck` & `test:unit`, commit `feat(web): add false-positive datasets page and upgrade algorithms & compute cluster telemetry`**

---

### Task 7: Frontend Multi-Channel Linkage (`/channels`) & User Workshop Permissions (`/users`)

**Files:**
- Modify: `web/src/views/channels/index.vue`
- Modify: `web/src/views/users/index.vue`
- Modify: `web/src/views/monitor/index.vue` (optional quick sound-light reset button if any `sound_light` channel exists or keep clean)

- [ ] **Step 1: Upgrade `web/src/views/channels/index.vue`**
  Support creating/editing all 4 channel types (`webhook`, `wecom`, `dingtalk`, `sound_light`) with type-specific config fields, `routing_rules` (`auto_route` switch + `severities`, `zone_ids`, `rule_modes` multi-select), connectivity Test, and **"Silence / Reset"** button for `sound_light` channels (`resetChannel`).
- [ ] **Step 2: Upgrade `web/src/views/users/index.vue`**
  Add **"Workshop Scope"** modal per non-admin user: `all_zones_access` switch + checkable spatial zone tree (`getUserZones` / `updateUserZones`).
- [ ] **Step 3: Verify `typecheck` & `test:unit`, commit `feat(web): upgrade notification channels with 4 templates/routing/reset and user workshop scope`**

---

### Task 8: Frontend Audit Logs (`/audit-logs`), System Settings & Health (`/settings`), & Full M3 Verification

**Files:**
- Create: `web/src/views/audit-logs/index.vue`
- Create: `web/src/views/settings/index.vue`
- Create: `web/src/views/settings/__tests__/settings.spec.ts`

- [ ] **Step 1: Build `web/src/views/audit-logs/index.vue`**
  Read-only paginated table filtered by actor, resource type, action, and time range, displaying summary, IP, Request ID, timestamp, and expandable sanitized JSON detail.
- [ ] **Step 2: Build `web/src/views/settings/index.vue` + `settings.spec.ts`**
  Platform configuration form (factory/platform title, default chime toggle, popup severity threshold, max export rows, timezone) + live System Health status cards (PostgreSQL, Redis, MediaMTX, Storage, Online Nodes & Cameras).
- [ ] **Step 3: Run full M3 verification suite across Backend, Frontend, and Python SDK**
  1. Backend: `buf lint`, `golangci-lint run ./...`, `golangci-lint run --build-tags=e2e ./tests/e2e/...`, `go test -count=1 ./...`, `go test -tags=e2e -v -count=1 ./tests/e2e/...`, `CGO_ENABLED=0 go build ./...`.
  2. Frontend: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit && pnpm.cmd --dir web build` + recursive zero-CDN scan on `web/dist/`.
  3. Python SDK: `python -m pytest runtime/python-sdk/tests -v`.
- [ ] **Step 4: Commit & push `feat/enterprise-m3` and run independent whole-branch review**
