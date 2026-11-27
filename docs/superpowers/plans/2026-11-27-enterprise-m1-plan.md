# AegisVision Enterprise Redesign — Milestone 1 (M1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Milestone 1 of the AegisVision enterprise factory EHS & SOP platform: the full enterprise database migration (`00002_enterprise_features.sql`), Factory → Workshop → Line/Workstation spatial tree (`zones`), operations overview dashboard (`/dashboard` with offline ECharts), professional 16-split duty monitor wall (`/monitor` with zone tree, tour presets, Web Audio chime, and quick-handle alerts), upgraded factory asset management (`/cameras` left-tree/right-table + batch/CSV import), and upgraded alarm console (`/alarms` with `ignored` status, `false_positive_reason`, batch closure, and streaming CSV/ZIP evidence export).

**Architecture:** Extends the existing 6-process pure-Go (`CGO_ENABLED=0`) backend and Vue Pure Admin (`i18n`) offline frontend with zero new infrastructure dependencies. All enterprise tables land once in `00002_enterprise_features.sql` (backward-compatible defaults) so M2 (`sop_sequence` + arming schedules + analytics) and M3 (false-positive datasets + multi-channel adapters + workshop RBAC + audit logs) build cleanly without breaking migrations.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), Gin, PostgreSQL 16 (`goose` + `sqlc`), Redis 7, RabbitMQ 3.13, MinIO/S3, Vue 3 + TypeScript + Element Plus + ECharts 6 (offline) + Tailwind CSS + Vitest.

**Spec:** [`docs/superpowers/specs/2026-11-27-enterprise-platform-redesign.md`](../specs/2026-11-27-enterprise-platform-redesign.md)

## Global Constraints

- Pure Go zero CGO (`CGO_ENABLED=0`); no C bindings; no `go:embed` of frontend static assets.
- Local Go module path MUST remain `aegisvision/backend` (zero `github.com/GuardHer` in Go imports).
- Strict 6-process package isolation (`api`, `scheduler`, `ingest`, `ws`, `notifier`/`evidence`, `agent` MUST NOT import each other; only `ingest` may import `rules`).
- All DDL changes MUST use `backend/migrations/` (`goose`); all SQL queries MUST use `sqlc` (`sqlc generate`); never hand-edit `sqlcgen/`.
- Structured JSON logging via `log/slog`; API errors via `domain.AppError` (never leak raw SQL/stack traces); no wild goroutines.
- Offline intranet zero-CDN guarantee in `web/`: no external font/script/SVG/audio URLs (use Web Audio API oscillator for duty chime, bundled ECharts 6 for charts).
- All new UI copy MUST be added to both `web/locales/zh-CN.yaml` and `web/locales/en.yaml`.
- All verification gates MUST pass: `golangci-lint run ./...`, `go test -count=1 ./...`, `pnpm.cmd --dir web typecheck && test:unit && build`.

## Review Focus

1. **Zone delete protection:** Deleting a zone that still has child zones OR bound cameras MUST return `409 Conflict` (`domain.CodeConflict`) without deleting anything (pinned in Task 2).
2. **Empty-database dashboard safety:** `GET /api/v1/dashboard/overview` with 0 cameras, 0 alarms, 0 nodes, and 0 `sop_cycles` MUST return `0` counts, `0.0` rates (never `NaN`/`Inf`), and non-null empty JSON arrays `[]` (pinned in Task 3).
3. **Shared monitor preset RBAC:** Non-admin (`operator`/`viewer`) attempting to create, update, or delete a shared preset (`is_shared: true`) or another user's private preset MUST receive `403 Forbidden` (pinned in Task 3).
4. **Alarm export cap & missing-snapshot resilience:** `POST /api/v1/alarms/export` MUST cap output to max 500 records per request and succeed cleanly even when some exported alarms have empty `image_object_key` or missing S3 objects (pinned in Task 4).
5. **Batch alarm handle atomicity & validation:** `POST /api/v1/alarms/batch-handle` with empty `alarm_ids` or invalid `handle_status` MUST return `400 Bad Request` with zero rows modified (pinned in Task 4).

---

## File Structure

### Backend (`backend/`)
- Create: `migrations/00002_enterprise_features.sql` — Full enterprise DDL (7 new tables: `zones`, `user_zones`, `arming_schedules`, `sop_cycles`, `monitor_presets`, `audit_logs`, `system_settings`; backward-compatible `ALTER TABLE` on `users`, `cameras`, `camera_rois`, `roi_algorithm_rules`, `alarms`, `alarm_channels`).
- Modify: `internal/domain/constants.go` — Add `RuleModeSOPSequence`, `HandleStatusIgnored`, `ZoneType*`, `FalsePositiveReason*`, `SOPOutcome*`.
- Create: `internal/platform/postgres/queries/zones.sql` — Zone CRUD, child/camera reference counts, camera batch update.
- Create: `internal/platform/postgres/queries/presets.sql` — `monitor_presets` CRUD scoped by owner/shared.
- Create: `internal/platform/postgres/queries/dashboard.sql` — Aggregated KPI, 7-day trend, top workshops, behavior breakdown, recent high-severity alarms.
- Modify: `internal/platform/postgres/queries/cameras.sql` — Include `zone_id`, `workstation_code`, `tags`, `last_online_at` in camera create/update/list queries.
- Modify: `internal/platform/postgres/queries/alarms.sql` — Support `zone_id`, `severity`, `keyword`, `false_positive_reason`, `sop_trace`, batch handle, and bounded export list.
- Modify: `internal/ingest/processor.go`, `internal/ingest/postgres_repo.go` — Denormalize `cameras.zone_id` onto persisted `alarms` row at creation time.
- Create: `internal/api/handlers_zones.go` + `handlers_zones_test.go` — Zone tree assembly & CRUD with delete guard.
- Create: `internal/api/handlers_presets.go` + `handlers_presets_test.go` — Monitor presets CRUD with owner/admin RBAC.
- Create: `internal/api/handlers_dashboard.go` + `handlers_dashboard_test.go` — Operations dashboard overview aggregation.
- Modify: `internal/api/handlers_cameras.go` — Support `zone_id`, `workstation_code`, `tags`, plus `BatchUpdate` & `ImportCSV`.
- Modify: `internal/api/handlers_alarms.go` + Create `handlers_alarms_export_test.go` — Support `ignored`, `false_positive_reason`, multi-filters (`zone_id`, `severity`, `keyword`), `BatchHandle`, and streaming CSV/ZIP `Export`.
- Modify: `internal/api/router.go` — Register all new M1 routes with RBAC.

### Frontend (`web/`)
- Modify: `locales/zh-CN.yaml`, `locales/en.yaml` — Add enterprise menu, dashboard, monitor, zone, preset, alarm batch/export copy.
- Modify: `src/router/modules/home.ts`, `src/router/modules/aegis.ts` — Re-root `/` to `/dashboard`; group menus under enterprise domains.
- Modify: `src/api/aegis.ts` — Add typed clients for zones tree/CRUD, dashboard overview, monitor presets, camera batch/import, alarm batch handle & blob download export.
- Create: `src/utils/alertSound.ts` — Pure Web Audio API oscillator duty alert chime (zero external audio files/CDN).
- Create: `src/views/dashboard/index.vue` + `__tests__/dashboard.spec.ts` — Operations Overview Dashboard (KPI cards, offline ECharts 7-day trend, workshop Top 10, behavior donut, recent critical/warning list).
- Modify: `src/views/monitor/index.vue` — Enterprise duty wall (collapsible Zone Tree + Tour Presets with auto-paging timer, 1/4/9/16 grid, fullscreen, local snapshot download, alert chime & popup, quick-handle alert cards).
- Modify: `src/views/cameras/index.vue` — Left Zone Tree CRUD + right camera table with workstation code, tags, batch enable/disable/move, CSV import dialog.
- Modify: `src/views/alarms/index.vue` — Zone cascade filter, severity/keyword filters, checkbox multi-select batch handle dialog (`confirmed`/`false_positive`/`ignored` + `false_positive_reason`), CSV/ZIP evidence export buttons.

---

### Task 1: Enterprise Database Migration (`00002`), Domain Constants & `sqlc` Queries

**Files:**
- Create: `backend/migrations/00002_enterprise_features.sql`
- Modify: `backend/internal/domain/constants.go`
- Create: `backend/internal/platform/postgres/queries/zones.sql`
- Create: `backend/internal/platform/postgres/queries/presets.sql`
- Create: `backend/internal/platform/postgres/queries/dashboard.sql`
- Modify: `backend/internal/platform/postgres/queries/cameras.sql`
- Modify: `backend/internal/platform/postgres/queries/alarms.sql`
- Test: `backend/internal/platform/postgres/migration_enterprise_test.go`

**Interfaces:**
- Produces:
  - `domain.RuleModeSOPSequence = "sop_sequence"`, `domain.HandleStatusIgnored = "ignored"`, `domain.ZoneTypeFactory/Workshop/Line/Workstation`, `domain.FalsePositiveReason*`, `domain.SOPOutcomeCompliant/Violated`
  - `sqlcgen.Zone`, `sqlcgen.MonitorPreset`, `sqlcgen.SopCycle`, `sqlcgen.ArmingSchedule`, `sqlcgen.AuditLog`, `sqlcgen.SystemSetting`
  - `sqlcgen.Queries` methods: `CreateZone`, `ListZones`, `GetZoneByID`, `UpdateZone`, `DeleteZone`, `CountChildZones`, `CountCamerasInZone`, `BatchUpdateCamerasEnabled`, `BatchUpdateCamerasZone`, `CreateMonitorPreset`, `ListMonitorPresetsForUser`, `GetMonitorPresetByID`, `UpdateMonitorPreset`, `DeleteMonitorPreset`, `BatchHandleAlarms`, `ListAlarmsForExport`, `GetDashboardKPIs`, `GetAlarmTrendByDay`, `GetTopZonesByAlarms`, `GetAlarmsByRuleMode`, `GetCameraHealthCounts`, `GetNodeHealthCounts`, `ListRecentHighSeverityAlarms`

- [ ] **Step 1: Write failing migration & sqlc test in `backend/internal/platform/postgres/migration_enterprise_test.go`**

```go
func TestEnterpriseMigration00002AppliesCleanly(t *testing.T) {
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AEGIS_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	defer store.Close()

	// Verify all 7 new enterprise tables exist and accept valid inserts/queries.
	for _, tbl := range []string{
		"zones", "user_zones", "arming_schedules", "sop_cycles",
		"monitor_presets", "audit_logs", "system_settings",
	} {
		var reg pgtype.Text
		if err := store.Pool().QueryRow(ctx, `SELECT to_regclass($1)::text`, tbl).Scan(&reg); err != nil || !reg.Valid {
			t.Fatalf("expected table %q to exist after migration 00002, err=%v", tbl, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test -v -run TestEnterpriseMigration00002AppliesCleanly ./internal/platform/postgres/...`
Expected: FAIL (`expected table "zones" to exist after migration 00002`).

- [ ] **Step 3: Create `backend/migrations/00002_enterprise_features.sql` and update `backend/internal/domain/constants.go`**

Implement exact DDL from Spec Section 4.1 & 4.2 (including `-- +goose Up` and `-- +goose Down`, updating `roi_algorithm_rules_rule_mode_check` to allow `'sop_sequence'`, adding `users.all_zones_access`, `cameras.zone_id/workstation_code/tags/last_online_at`, `camera_rois.color`, `roi_algorithm_rules.schedule_id`, `alarms.zone_id/false_positive_reason/sop_trace`, `alarm_channels.routing_rules`). Add domain constants in `internal/domain/constants.go`.

- [ ] **Step 4: Author `zones.sql`, `presets.sql`, `dashboard.sql`, update `cameras.sql` & `alarms.sql`, and run `sqlc generate`**

Write clean parameterized PostgreSQL queries (no `SELECT *` ambiguity on joins; use `COALESCE` for aggregates so zero-row queries return `0`). Regenerate `sqlcgen/` via `sqlc generate`, and update any existing callers whose `sqlcgen` param structs gained new fields (`CreateCameraParams`, `UpdateCameraParams`, `CreateAlarmParams`, `ListAlarmsParams`, `CountAlarmsParams`, `HandleAlarmParams`).

- [ ] **Step 5: Run tests and lint to verify Task 1 passes**

Run: `cd backend && sqlc generate && golangci-lint run ./... && go test -count=1 ./...`
Expected: 0 lint issues; all packages PASS including `TestEnterpriseMigration00002AppliesCleanly`.

- [ ] **Step 6: Commit**

```bash
git add backend/migrations/00002_enterprise_features.sql backend/internal/domain/ backend/internal/platform/postgres/ backend/internal/api/ backend/internal/ingest/
git commit -m "feat(db): add enterprise migration 00002, domain constants, and m1 sqlc queries"
```

---

### Task 2: Zones Spatial Tree & Enterprise Camera Management APIs

**Files:**
- Create: `backend/internal/api/handlers_zones.go`
- Create: `backend/internal/api/handlers_zones_test.go`
- Modify: `backend/internal/api/handlers_cameras.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: `sqlcgen.Queries` zone & camera methods from Task 1.
- Produces:
  - `ZonesHandler` (`GetTree`, `Create`, `Update`, `Delete`) mounted at `GET /api/v1/zones/tree`, `POST /api/v1/zones`, `PUT /api/v1/zones/:id`, `DELETE /api/v1/zones/:id`
  - `CamerasHandler.BatchUpdate` (`POST /api/v1/cameras/batch` — actions `enable`, `disable`, `move_zone`)
  - `CamerasHandler.ImportCSV` (`POST /api/v1/cameras/import` — imports `name,rtsp_url,sub_stream_url,workstation_code,zone_id,codec_hint`)
  - `ZoneTreeNode` JSON response shape with recursive `children`, `camera_count`, `online_camera_count` (rolled up from descendants).

- [ ] **Step 1: Write failing tests in `backend/internal/api/handlers_zones_test.go`**

Write tests covering:
- `TestZonesTreeHierarchyAndCounts`: creates `factory -> workshop -> workstation`, binds 1 online + 1 offline camera to workstation, asserts `GET /api/v1/zones/tree` rolls up `camera_count=2`, `online_camera_count=1` to both workshop and factory.
- `TestZoneDeleteRejectsChildrenOrCameras` *(Review Focus #1)*: asserts `DELETE /api/v1/zones/:id` returns `409 Conflict` when a zone has child zones OR bound cameras, and returns `200 OK` once empty.
- `TestCamerasBatchUpdateAndCSVImport`: creates 2 cameras, runs `POST /api/v1/cameras/batch` (`action: "move_zone"` and `action: "disable"`), and imports 2 cameras via CSV verifying created count & error list on invalid rows.

- [ ] **Step 2: Run tests to verify failure**

Run: `cd backend && go test -v -run "TestZone|TestCamerasBatch" ./internal/api/...`
Expected: FAIL (handlers undefined).

- [ ] **Step 3: Implement `handlers_zones.go`, extend `handlers_cameras.go`, and wire routes in `router.go`**

- Validate `zone_type` against `factory`/`workshop`/`line`/`workstation`; prevent parent cycle on `UpdateZone`.
- Roll up `camera_count` and `online_camera_count` bottom-up across the tree in memory (`O(N)`).
- Enforce `409 Conflict` (`domain.NewConflictError`) on `Delete` if `CountChildZones > 0 || CountCamerasInZone > 0`.
- Extend `CreateCamera` / `UpdateCamera` JSON payloads with optional `zone_id`, `workstation_code`, `tags`; emit config events on batch/import changes.

- [ ] **Step 4: Run tests and lint**

Run: `cd backend && golangci-lint run ./internal/api/... && go test -v -count=1 ./internal/api/...`
Expected: 0 issues, PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers_zones.go backend/internal/api/handlers_zones_test.go backend/internal/api/handlers_cameras.go backend/internal/api/router.go
git commit -m "feat(api): add spatial zones tree crud, camera batch operations, and csv import"
```

---

### Task 3: Monitor Presets & Operations Dashboard Overview APIs

**Files:**
- Create: `backend/internal/api/handlers_presets.go`
- Create: `backend/internal/api/handlers_presets_test.go`
- Create: `backend/internal/api/handlers_dashboard.go`
- Create: `backend/internal/api/handlers_dashboard_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: `sqlcgen.Queries` preset & dashboard methods from Task 1.
- Produces:
  - `PresetsHandler` (`List`, `Create`, `Update`, `Delete`) mounted at `/api/v1/monitor/presets`
  - `DashboardHandler` (`Overview`) mounted at `GET /api/v1/dashboard/overview` returning:
    - `kpis`: `{today_alarms, yesterday_alarms, unhandled_alarms, confirmed_rate, false_positive_rate, today_sop_violations, sop_compliance_rate, cameras_online, cameras_total, nodes_online, nodes_total, running_tasks}`
    - `trend_7d`: `[{date, safety_count, sop_count}]` (always 7 contiguous calendar days, zero-filled)
    - `top_zones`: `[{zone_id, zone_name, alarm_count}]`
    - `by_rule_mode`: `[{rule_mode, count}]`
    - `recent_alerts`: up to 10 recent unhandled `critical`/`warning` alarms

- [ ] **Step 1: Write failing tests in `handlers_presets_test.go` and `handlers_dashboard_test.go`**

- `TestMonitorPresetsRBACAndValidation` *(Review Focus #3)*:
  - `operator` can create/list/delete own private preset (`is_shared: false`, `grid_size: 9`, `tour_interval_seconds: 30`);
  - `operator` creating `is_shared: true` OR updating/deleting another user's preset returns `403 Forbidden`;
  - `admin` creating `is_shared: true` returns `201 Created`, visible to `viewer`;
  - Invalid `grid_size: 5` returns `400 Bad Request`.
- `TestDashboardOverviewEmptyAndPopulated` *(Review Focus #2)*:
  - Asserts empty DB / isolated clean query returns `200 OK`, `trend_7d` length 7, non-null `[]` slices, and finite `0.0` rates (no `NaN`);
  - Inserts sample alarms (`anomaly_detect`, `sop_sequence`), `sop_cycles` (3 compliant, 1 violated -> `sop_compliance_rate == 75.0`), and verifies accurate KPIs.

- [ ] **Step 2: Run tests to verify failure**

Run: `cd backend && go test -v -run "TestMonitorPresets|TestDashboardOverview" ./internal/api/...`
Expected: FAIL.

- [ ] **Step 3: Implement `handlers_presets.go`, `handlers_dashboard.go`, and mount routes in `router.go`**

- Validate `grid_size` in `{1, 4, 9, 16}` and `tour_interval_seconds >= 0`.
- In `DashboardHandler.Overview`, compute safe division helpers (`safeRate(num, den float64) float64`) and always initialize slices (`make(..., 0)`) so JSON never serializes `null`.

- [ ] **Step 4: Run tests and lint**

Run: `cd backend && golangci-lint run ./internal/api/... && go test -v -count=1 ./internal/api/...`
Expected: 0 issues, PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers_presets.go backend/internal/api/handlers_presets_test.go backend/internal/api/handlers_dashboard.go backend/internal/api/handlers_dashboard_test.go backend/internal/api/router.go
git commit -m "feat(api): add monitor tour presets and operations dashboard overview endpoints"
```

---

### Task 4: Alarm Batch Closure, `ignored` / `false_positive_reason`, `zone_id` Denormalization & Streaming CSV/ZIP Export

**Files:**
- Modify: `backend/internal/ingest/processor.go`
- Modify: `backend/internal/ingest/postgres_repo.go`
- Modify: `backend/internal/api/handlers_alarms.go`
- Create: `backend/internal/api/handlers_alarms_export_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: `sqlcgen.Queries` alarm batch & export queries from Task 1, `storage.ObjectStorage`.
- Produces:
  - `POST /api/v1/alarms/batch-handle` (`{alarm_ids: string[], handle_status: "confirmed"|"false_positive"|"ignored", false_positive_reason?: string, handle_note?: string}`)
  - `POST /api/v1/alarms/export` (`{format: "csv"|"zip", zone_id?, camera_id?, rule_mode?, severity?, handle_status?, keyword?, start_time?, end_time?, limit?}`) — returns streaming `text/csv` or `application/zip` (hard-capped at 500 records)
  - `GET /api/v1/alarms` extended with optional `zone_id`, `severity`, `keyword` filters
  - `POST /api/v1/alarms/:id/handle` extended to accept `ignored` and `false_positive_reason`
  - `aegis-ingest` automatically populates `alarms.zone_id` from `cameras.zone_id` on insert.

- [ ] **Step 1: Write failing tests in `backend/internal/api/handlers_alarms_export_test.go`**

- `TestAlarmBatchHandleValidationAndUpdate` *(Review Focus #5)*:
  - Empty `alarm_ids` or invalid `handle_status: "unknown"` returns `400 Bad Request`;
  - Valid batch of 2 alarms with `handle_status: "false_positive"`, `false_positive_reason: "lighting"` updates both rows atomically and returns `updated_count: 2`.
- `TestAlarmExportCSVAndZIPResilience` *(Review Focus #4)*:
  - Creates 2 alarms (1 with real JPEG stored in local/S3 `ObjectStorage`, 1 with empty `image_object_key`);
  - `POST /api/v1/alarms/export` with `format: "csv"` returns `200 OK`, `Content-Type: text/csv`, header row + 2 data rows;
  - `POST /api/v1/alarms/export` with `format: "zip"` returns `200 OK`, valid ZIP archive containing `metadata.json` (2 items) and 1 `images/<alarm_id>.jpg` without failing on the snapshot-less alarm;
  - Passing `limit: 9999` is clamped to `500`.

- [ ] **Step 2: Run tests to verify failure**

Run: `cd backend && go test -v -run "TestAlarmBatchHandle|TestAlarmExport" ./internal/api/...`
Expected: FAIL.

- [ ] **Step 3: Update `ingest/postgres_repo.go`, `api/handlers_alarms.go`, and `api/router.go`**

- Pass `storage.ObjectStorage` into `NewAlarmsHandler` in `router.go`.
- Use `encoding/csv` (with UTF-8 BOM `\xEF\xBB\xBF` so Excel opens Chinese cleanly) and `archive/zip` streaming directly to `c.Writer` (one JPEG loaded & closed per iteration, never buffering the entire archive in RAM).
- In `ingest/postgres_repo.go`, resolve `cam.ZoneID` when inserting `alarms`.

- [ ] **Step 4: Run backend lint & full tests**

Run: `cd backend && golangci-lint run ./... && go test -count=1 ./...`
Expected: 0 issues, all 16+ packages PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/ingest/ backend/internal/api/handlers_alarms.go backend/internal/api/handlers_alarms_export_test.go backend/internal/api/router.go
git commit -m "feat(alarms): add zone denormalization, batch closure, false-positive reason, and streaming csv/zip export"
```

---

### Task 5: Frontend Routing, i18n, API Clients & Operations Overview Dashboard (`/dashboard`)

**Files:**
- Modify: `web/locales/zh-CN.yaml`, `web/locales/en.yaml`
- Modify: `web/src/router/modules/home.ts`, `web/src/router/modules/aegis.ts`
- Modify: `web/src/api/aegis.ts`
- Create: `web/src/views/dashboard/index.vue`
- Create: `web/src/views/dashboard/__tests__/dashboard.spec.ts`

**Interfaces:**
- Consumes: `GET /api/v1/dashboard/overview`, `GET /api/v1/zones/tree`, `/api/v1/monitor/presets`, `/api/v1/cameras/batch`, `/api/v1/cameras/import`, `/api/v1/alarms/batch-handle`, `/api/v1/alarms/export`.
- Produces:
  - Typed TypeScript API functions & interfaces in `web/src/api/aegis.ts` for all M1 endpoints
  - Enterprise route structure with `/` → `/dashboard` (rank 0), `/monitor` (rank 1), `/alarms` (rank 2), `/cameras` (rank 3), `/algorithms` (rank 4), `/nodes` (rank 5), `/channels` (rank 6), `/users` (rank 7)
  - Offline ECharts 6 dashboard view rendering 6 KPI cards, 7-day Safety vs. SOP trend chart, Top-10 Workshop horizontal bar chart, Behavior distribution donut chart, and Recent High-Severity Alerts quick-action list.

- [ ] **Step 1: Write failing unit test in `web/src/views/dashboard/__tests__/dashboard.spec.ts`**

Mount `Dashboard` with mocked `getDashboardOverview` returning sample KPIs (including `today_alarms: 12`, `unhandled_alarms: 3`, `sop_compliance_rate: 94.5`) and verify KPI values, empty-state resilience, and quick navigation to `/alarms`.

- [ ] **Step 2: Run unit test to verify failure**

Run: `pnpm.cmd --dir web test:unit`
Expected: FAIL (`Cannot find module '../index.vue'`).

- [ ] **Step 3: Update `zh-CN.yaml`, `en.yaml`, `router/modules/home.ts`, `router/modules/aegis.ts`, `api/aegis.ts`, and build `views/dashboard/index.vue`**

- Import ECharts cleanly from `echarts` (bundled offline, zero CDN), dispose instances on `onBeforeUnmount`, resize cleanly via `ResizeObserver`.
- Clicking the "Unhandled Alarms" KPI card navigates to `/alarms/index?handle_status=unhandled`.

- [ ] **Step 4: Run typecheck & unit tests**

Run: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit`
Expected: 0 TS errors, all unit tests PASS.

- [ ] **Step 5: Commit**

```bash
git add web/locales/ web/src/router/modules/ web/src/api/aegis.ts web/src/views/dashboard/
git commit -m "feat(web): add operations overview dashboard, enterprise routing, and m1 api clients"
```

---

### Task 6: Frontend Enterprise Duty Monitor Center (`/monitor`)

**Files:**
- Create: `web/src/utils/alertSound.ts`
- Modify: `web/src/views/monitor/index.vue`
- Create: `web/src/views/monitor/__tests__/monitor.spec.ts`

**Interfaces:**
- Consumes: `getZonesTree`, `listCameras`, `listMonitorPresets`, `createMonitorPreset`, `deleteMonitorPreset`, `handleAlarm` from `web/src/api/aegis.ts`; `playDutyAlertChime` from `web/src/utils/alertSound.ts`.
- Produces:
  - Collapsible left sidebar with Tab 1「点位树」(search + online/offline status dots + click to assign active slot) and Tab 2「轮巡方案」(save current split & cameras as private/shared preset, load preset, toggle timed tour pagination every `tour_interval_seconds`).
  - Center video wall supporting **1 / 4 / 9 / 16** split grid, main/sub stream toggle, overlay toggle, mute/unmute toggle, fullscreen toggle, per-tile local Canvas+Video snapshot PNG capture, and clean `ResizeObserver` alignment.
  - Collapsible right realtime alert stream with severity filter (`all` / `critical` / `warning`), Web Audio chime + non-blocking `ElNotification` on `critical` alerts when unmuted, and one-click「确认」「误报」inline buttons for `admin`/`operator`.

- [ ] **Step 1: Write failing unit test in `web/src/views/monitor/__tests__/monitor.spec.ts`**

Verify 1/4/9/16 grid switching (`gridSize = 16` renders 16 tile slots), mute toggle state, and zone tree search filtering.

- [ ] **Step 2: Run unit test to verify failure**

Run: `pnpm.cmd --dir web test:unit`
Expected: FAIL.

- [ ] **Step 3: Implement `web/src/utils/alertSound.ts` and upgrade `web/src/views/monitor/index.vue`**

- `alertSound.ts`: synthesize a brief two-tone beep using `window.AudioContext` (catch autoplay errors gracefully; zero external `.mp3`/CDN).
- Ensure tour timer (`setInterval`) and `WebSocket` and `ResizeObserver` are all cleanly cleared in `onBeforeUnmount`.

- [ ] **Step 4: Run typecheck & unit tests**

Run: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/alertSound.ts web/src/views/monitor/
git commit -m "feat(web): upgrade duty monitor wall with zone tree, 16-split grid, tour presets, chime, and quick handling"
```

---

### Task 7: Frontend Factory Asset Management (`/cameras`) & Enterprise Alarm Console (`/alarms`) + Full M1 Gate

**Files:**
- Modify: `web/src/views/cameras/index.vue`
- Modify: `web/src/views/alarms/index.vue`

**Interfaces:**
- Consumes: `getZonesTree`, `createZone`, `updateZone`, `deleteZone`, `batchUpdateCameras`, `importCamerasCSV`, `batchHandleAlarms`, `exportAlarms` from `web/src/api/aegis.ts`.
- Produces:
  - `/cameras`: Left interactive `el-tree` for Factory → Workshop → Line/Workstation CRUD + right filtered camera table showing workstation code, tags, online badge, batch enable/disable/move-zone toolbar, and CSV batch import modal with template download & row-error feedback.
  - `/alarms`: Zone cascade selector, severity & keyword search, multi-select checkbox column + batch closure modal (`confirmed`/`false_positive`/`ignored` + `false_positive_reason` selector), CSV & ZIP evidence package download buttons, and updated detail drawer showing `false_positive_reason` and `ignored` option.

- [ ] **Step 1: Upgrade `web/src/views/cameras/index.vue`**

Add left Zone Tree panel (create root/child zone, edit, delete with conflict toast), camera form fields for `zone_id` / `workstation_code` / `tags`, batch selection bar (`enable`, `disable`, `move_zone`), and CSV import dialog.

- [ ] **Step 2: Upgrade `web/src/views/alarms/index.vue`**

Add URL query sync (`?handle_status=unhandled` from Dashboard KPI click), zone cascade + severity + keyword filters, multi-select batch handle modal with `false_positive_reason` dropdown (`lighting`, `occlusion`, `similar_action`, `model_drift`, `other`), and CSV/ZIP Blob download triggers.

- [ ] **Step 3: Run Full M1 Verification Gate (Backend + E2E + Python + Frontend Build + Zero-CDN Check)**

Run:
1. `cd backend && golangci-lint run ./... && golangci-lint run --build-tags=e2e ./tests/e2e/...`
2. `cd backend && go test -count=1 ./...` (against live VM `192.168.0.114`)
3. `cd backend && go test -tags=e2e -v -count=1 ./tests/e2e/...`
4. `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit && pnpm.cmd --dir web build`
5. Verify zero external CDN URLs in `web/dist/`.

Expected: ALL PASS with 0 lint/TS issues and 0 external URLs.

- [ ] **Step 4: Commit**

```bash
git add web/src/views/cameras/index.vue web/src/views/alarms/index.vue
git commit -m "feat(web): upgrade factory asset tree management and enterprise alarm console with batch export"
```
