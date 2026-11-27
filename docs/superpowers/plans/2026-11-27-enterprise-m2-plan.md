# AegisVision Enterprise Redesign — Milestone 2 (M2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Milestone 2 (M2) of the AegisVision enterprise redesign: reusable arming schedules (with overnight shifts and holiday exceptions), a multi-step workstation `sop_sequence` state machine with live monitor-wall step progress bars and alarm violation timelines, async `ffmpeg` single-frame snapshot fallback for snapshot-less stateful alarms, and an offline ECharts analytics & reporting center.

**Architecture:** Extends the existing 6-process pure-Go (`CGO_ENABLED=0`) backend and Vue Pure Admin offline frontend without adding any new middleware or breaking package isolation (`internal/api`, `internal/ingest`, `internal/evidence`, `internal/rules` remain strictly decoupled). All database tables (`arming_schedules`, `sop_cycles`, `alarms.sop_trace`, `roi_algorithm_rules.schedule_id`) already exist from migration `00002_enterprise_features.sql`, so M2 requires zero schema migrations.

**Tech Stack:** Go 1.24 (`CGO_ENABLED=0`, Gin, `pgx/v5`, `sqlc`, `time/tzdata`, external `ffmpeg` CLI), PostgreSQL 16, Redis Pub/Sub, RabbitMQ, Python 3.13 (`aegis_runtime_sdk`), Vue 3 + TypeScript + Element Plus + ECharts v6 (offline) + Vitest.

**Spec:** [`docs/superpowers/specs/2026-11-27-enterprise-platform-redesign.md`](../specs/2026-11-27-enterprise-platform-redesign.md)

## Global Constraints

- Pure Go zero CGO (`CGO_ENABLED=0`); external `ffmpeg` CLI is allowed only inside `aegis-dispatcher` (`internal/evidence`) for async MP4 clips and single-frame JPEG snapshot fallback.
- Local Go module path stays `aegisvision/backend`; all SQL queries go through `sqlc` (`jsonb -> encoding/json.RawMessage` already configured in `backend/sqlc.yaml`).
- Process packages (`api`, `scheduler`, `ingest`, `ws`, `notifier`/`evidence`, `agent`) must never directly `import` each other (only `ingest` imports pure-logic `rules`).
- Transient real-time `sop_progress` flows ONLY via `aegis-ingest` → Redis Pub/Sub → `aegis-ws` (never written to RabbitMQ or Agent SQLite).
- Snapshot fallback and MP4 recording MUST NEVER block alarm persistence, WS alert broadcast, or Webhook delivery.
- Heartbeat timeout guard (`heartbeat_timeout_seconds`, default 5s): both `absence_detect` and `sop_sequence` MUST pause/reset timers when frame gap exceeds heartbeat timeout so stream drops or node outages never cause false timeout alarms.
- Zero external CDN dependencies in `web/dist/`; all new UI copy synced in both `web/locales/zh-CN.yaml` and `web/locales/en.yaml`.

## Review Focus

1. **Overnight shift + excluded holiday boundary**: A night shift `20:00–08:00` starting on `2026-09-30` MUST disarm immediately at `2026-10-01 00:00` when `2026-10-01` is in `exclude_dates` (pinned in Task 1 tests).
2. **SOP stream-drop false-timeout prevention & optional step skipping**: A frame gap `> heartbeat_timeout_seconds` pauses/resets SOP step elapsed timers without firing `step_timeout`; skipping `required: false` steps never triggers `missing_step`/`out_of_order` (pinned in Task 2 tests).
3. **`zone_entry` re-arm guard**: After a `zone_entry` SOP cycle completes or violates, a stationary person in the ROI MUST NOT immediately re-trigger a new cycle until exiting and re-entering OR `rearm_cooldown_seconds` elapses (pinned in Task 2 tests).
4. **Snapshot fallback resilience**: When `aegis-dispatcher` handles a `mode="snapshot"` job and `ffmpeg` fails (e.g., camera offline), it logs a warning, leaves `alarms.image_object_key` empty, and ACKs cleanly without poisoning `evidence.record` or mutating `video_status` (pinned in Task 3 tests).
5. **Analytics division-by-zero & CSV formula neutralization**: Empty date ranges or zero-alarm zones return finite `0.0` rates and valid empty arrays (never `NaN`/`null`), and exported analytics CSV neutralizes leading `=`, `+`, `-`, `@` characters (pinned in Task 4 tests).

---

### Task 1: Arming Schedules Evaluator, SQLC Queries, & Schedule/Rule REST API

**Files:**
- Create: `backend/internal/rules/schedule.go`
- Create: `backend/internal/rules/schedule_test.go`
- Create: `backend/internal/platform/postgres/queries/schedules.sql`
- Create: `backend/internal/api/handlers_schedules.go`
- Create: `backend/internal/api/handlers_schedules_test.go`
- Modify: `backend/internal/rules/engine.go`
- Modify: `backend/internal/platform/postgres/queries/rules.sql`
- Modify: `backend/internal/api/handlers_rules.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Produces (`rules`):
  - `type WeeklyWindow struct { Days []int; Start string; End string }`
  - `type IncludeDateWindow struct { Date string; Start string; End string }`
  - `type ArmingSchedule struct { ID, Name, Timezone string; WeeklyWindows []WeeklyWindow; ExcludeDates []string; IncludeDates []IncludeDateWindow; Enabled bool }`
  - `func ParseArmingSchedule(id, name, tz string, enabled bool, weeklyJSON, excludeJSON, includeJSON []byte) (*ArmingSchedule, error)`
  - `func (s *ArmingSchedule) IsArmed(ts time.Time) bool`
  - `Rule.Schedule *ArmingSchedule`
- Produces (`api`):
  - `GET /api/v1/schedules` (all roles), `POST /api/v1/schedules`, `PUT /api/v1/schedules/:id`, `DELETE /api/v1/schedules/:id` (admin only; transactional `409 Conflict` if referenced by any rule).
  - `POST /api/v1/cameras/:id/rules` and `PUT /api/v1/rules/:rule_id` accept optional `schedule_id` and `rule_mode = "sop_sequence"` with `ValidateSOPParams(params)`.

- [ ] **Step 1: Write failing unit tests for `ArmingSchedule.IsArmed` and disarmed state reset**

Create `backend/internal/rules/schedule_test.go` testing:
1. Weekday daytime window `08:00–20:00` in `Asia/Shanghai` (armed at `10:00`, disarmed at `21:00`, disarmed on Sunday).
2. Overnight window `20:00–08:00` on Mon–Fri (armed Friday `23:30` and Saturday `02:00`, disarmed Saturday `09:00`).
3. Review Focus #1: `exclude_dates: ["2026-10-01"]` overrides both previous-night `20:00–08:00` shift at `2026-10-01 00:05` AND `include_dates` for the same date.
4. `include_dates` with explicit `start`/`end` and omitted `start`/`end` (full day `00:00–24:00`).
5. `Engine.Evaluate` during disarmed window resets `consecutiveHits`/`absentElapsed` so re-entering armed window does not immediately fire on stale state, while still returning live `ROICounts`.

- [ ] **Step 2: Run unit tests to verify failure**

Run: `go test ./internal/rules/... -run TestArmingSchedule`
Expected: FAIL (`ArmingSchedule` undefined).

- [ ] **Step 3: Implement `backend/internal/rules/schedule.go` and hook into `engine.go`**

Implement `ArmingSchedule`, `ParseArmingSchedule` (using embedded `_ "time/tzdata"`), and `IsArmed(ts time.Time) bool` with priority: when `rule.Schedule == nil`, the rule is armed 24×7; when `schedule.Enabled == false`, `IsArmed` returns `false` (template paused/disarmed); otherwise evaluate `exclude_dates` > `include_dates` > `weekly_windows`. In `engine.go`, add `Schedule *ArmingSchedule` to `Rule`; in `Evaluate`, if `rule.Schedule != nil && !rule.Schedule.IsArmed(ts)`, reset `b.states[rule.ID]` (preserving only `lastAlarmAt`) and `continue` after recording live `ROICounts`.

- [ ] **Step 4: Run `internal/rules` tests to verify pass**

Run: `go test -v ./internal/rules/...`
Expected: PASS.

- [ ] **Step 5: Add `schedules.sql`, update `rules.sql` (`schedule_id`), run `sqlc generate`, implement `handlers_schedules.go` + `handlers_rules.go` updates + `handlers_schedules_test.go`**

- `schedules.sql`: `CreateArmingSchedule`, `GetArmingScheduleByID`, `ListArmingSchedules` (returning `rule_count` via `LEFT JOIN roi_algorithm_rules`), `UpdateArmingSchedule`, `DeleteArmingSchedule`, `CountRulesUsingSchedule`.
- `rules.sql`: include `schedule_id` in `CreateROIAlgorithmRule` and `UpdateROIAlgorithmRule`.
- `handlers_schedules.go`: validate non-empty name, valid IANA timezone, valid `HH:MM` windows (`days` in `1..7`), valid `YYYY-MM-DD` dates; transactional `Delete` returning `409 Conflict` if `CountRulesUsingSchedule > 0`; emit `config_event` (`schedule`, `created/updated/deleted`) so `aegis-ingest` hot-reloads schedules!
- `handlers_rules.go`: allow `domain.RuleModeSOPSequence`, parse optional `schedule_id`, validate `sop_sequence` params (at least 1 required step, unique `step_id`, valid `trigger_mode`, non-empty `labels`, `min_confidence` in `[0, 1]`).
- `handlers_schedules_test.go`: test CRUD, invalid `HH:MM` / timezone (`400`), binding a rule to a schedule, `409 Conflict` deleting a referenced schedule, unbinding & deleting cleanly (`200`), and creating a `sop_sequence` rule (`201`).

- [ ] **Step 6: Verify Task 1 tests & commit**

Run: `go test -count=1 ./internal/rules/... ./internal/api/... -run "TestArmingSchedule|TestSchedulesCRUD"`
Expected: PASS. Commit `feat(schedules): add arming schedule evaluator, sqlc queries, and rest crud`.

---

### Task 2: Multi-Step SOP State Machine (`internal/rules/sop.go`)

**Files:**
- Create: `backend/internal/rules/sop.go`
- Create: `backend/internal/rules/sop_test.go`
- Modify: `backend/internal/rules/engine.go`

**Interfaces:**
- Produces (`rules`):
  - `type SOPStepSpec struct { StepID, Name string; Labels []string; ROIID string; MinConfidence float64; HitPoint string; MinDwellFrames, MaxWaitSeconds int; Required bool }`
  - `type SOPParams struct { TriggerMode string; EntryLabels []string; RearmMode string; RearmCooldownSeconds, CycleTimeoutSeconds int; ViolationTypes []string; Steps []SOPStepSpec }`
  - `type SOPStepTraceItem struct { StepID, Name, Status string; CompletedAt *time.Time; ElapsedMs int64 }`
  - `type SOPTrace struct { TriggerMode, ViolationType, ViolatedStepID, ViolatedStepName string; StartedAt, EndedAt time.Time; DurationMs int64; Steps []SOPStepTraceItem }`
  - `type SOPRuleProgress struct { RuleID, RuleName, State string; CurrentStepIndex int; Steps []SOPStepTraceItem }`
  - `type CompletedSOPCycle struct { RuleID, CameraID, AlgorithmVersionID string; StartedAt, EndedAt time.Time; DurationMs int64 }`
  - `Rule.CameraROIs map[string]ROI` (optional per-step ROI lookup map on same camera)
  - `EvalOutput.SOPProgress []SOPRuleProgress`, `EvalOutput.CompletedSOPCycles []CompletedSOPCycle`
  - `TriggeredAlarm.SOPTrace *SOPTrace`, `TriggeredAlarm.ViolationType string`

- [ ] **Step 1: Write failing unit tests in `backend/internal/rules/sop_test.go`**

Write tests covering every spec requirement and Review Focus #2 & #3:
1. `TestSOPFirstStepCompliantCycle`: 3-step sequence (including cross-ROI step 2 via `Rule.CameraROIs`) with `min_dwell_frames=2` -> emits intermediate `SOPProgress` (`in_progress`) and final `CompletedSOPCycle` (`DurationMs > 0`) with zero alarms.
2. `TestSOPOptionalStepSilentlySkipped` (Review Focus #2): Steps `[req_1, opt_2(required=false), req_3]` -> completing `req_1` then `req_3` completes cleanly with `opt_2` marked `"skipped"`, zero alarms; waiting past `opt_2.max_wait_seconds` never fires `step_timeout`.
3. `TestSOPMissingStepAndOutOfOrder`: Skipping a `required: true` step triggers `TriggeredAlarm` with `RuleMode == "sop_sequence"`, non-nil `SOPTrace`, `ViolationType == "missing_step"` / `"out_of_order"`, and resets cycle.
4. `TestSOPStepTimeoutAndHeartbeatGapPause` (Review Focus #2):
   - Normal heartbeats every 1s past `max_wait_seconds=5` fires `step_timeout` alarm with `SOPTrace`.
   - Frame gap of `12s > heartbeat_timeout_seconds(5s)` pauses/resets timer and fires ZERO alarms.
5. `TestSOPZoneEntryRearmProtection` (Review Focus #3): `trigger_mode="zone_entry"` starts cycle on `person` detection; after cycle finishes (or violates), keeping `person` continuously in ROI does NOT start a second cycle until either `person` exits for 1 frame and re-enters OR `rearm_cooldown_seconds` passes.
6. `TestSOPDisarmedScheduleResetsInProgressCycle`: Switching from armed to disarmed clears in-progress SOP cycle cleanly.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/rules/... -run TestSOP`
Expected: FAIL (`SOPStepSpec` / `SOPProgress` undefined).

- [ ] **Step 3: Implement `backend/internal/rules/sop.go` and wire into `engine.go`**

Implement `evaluateSOPRule(rule Rule, st *ruleRuntimeState, frame DetectionFrame, ts time.Time) (alarm *TriggeredAlarm, completed *CompletedSOPCycle, progress SOPRuleProgress)` inside `internal/rules/sop.go` with clean state transitions (`idle`, `in_progress`, `rearm`), per-step ROI lookup (`rule.CameraROIs[step.ROIID]` fallback to `rule.ROI`), dwell counters, heartbeat gap validation, and structured `SOPTrace` / `SOPRuleProgress` generation.

- [ ] **Step 4: Run all `internal/rules` tests with `-race` / `-count=1`**

Run: `go test -v -count=1 ./internal/rules/...`
Expected: PASS.

- [ ] **Step 5: Commit**

Commit `feat(rules): implement single-workstation multi-step sop_sequence state machine`.

---

### Task 3: Ingest SOP Persistence, Real-Time `sop_progress` Broadcast, & Async Snapshot Fallback

**Files:**
- Create: `backend/internal/platform/postgres/queries/sop_cycles.sql`
- Modify: `backend/internal/ingest/processor.go`
- Modify: `backend/internal/ingest/postgres_repo.go`
- Create: `backend/internal/ingest/sop_ingest_test.go`
- Modify: `backend/internal/evidence/recorder.go`
- Modify: `backend/internal/evidence/postgres_repo.go`
- Modify: `backend/internal/evidence/recorder_test.go`

**Interfaces:**
- Consumes: `rules.EvalOutput.SOPProgress`, `rules.EvalOutput.CompletedSOPCycles`, `rules.TriggeredAlarm.SOPTrace`, `rules.ParseArmingSchedule`.
- Produces:
  - `wsFrameMessage.SOPProgress []rules.SOPRuleProgress` (`json:"sop_progress,omitempty"`).
  - `evidence.RecordRequest.Mode string` (`""`/`"video"` = MP4 clip; `"snapshot"` = single JPEG grab via `ffmpeg -frames:v 1 -q:v 3`, uploaded to `images/...` and backfilled to `alarms.image_object_key`).
  - `ingest.Repository.RecordCompliantSOPCycle(ctx, cycle PersistedSOPCycle) error`.

- [ ] **Step 1: Add `sop_cycles.sql` and run `sqlc generate`**

Add `CreateSOPCycle :one` inserting `(id, rule_id, camera_id, zone_id, algorithm_version_id, outcome, violation_type, alarm_id, started_at, ended_at, duration_ms)`.

- [ ] **Step 2: Write failing tests for async snapshot fallback & ingest SOP flow**

1. In `backend/internal/evidence/recorder_test.go`:
   - `TestRecorderSnapshotFallbackUploadsAndBackfills`: `HandleMessage` with `{"mode":"snapshot","alarm_id":...,"camera_id":...}` invokes `SnapshotGenerator`, uploads JPEG to storage, calls `UpdateAlarmImageEvidence` (NOT `UpdateAlarmVideoEvidence`).
   - `TestRecorderSnapshotFallbackFailureDoesNotMutateVideoStatus` (Review Focus #4): when `SnapshotGenerator` returns error, `UpdateAlarmVideoEvidence` is NOT called and error is returned cleanly for `aegis-dispatcher` to log & ACK.
2. In `backend/internal/ingest/sop_ingest_test.go`:
   - `TestProcessorSOPProgressBroadcastAndCyclePersistence`: verifies `wsFrameMessage` includes `sop_progress`, compliant cycles call `RecordCompliantSOPCycle`, violated cycles persist `sop_trace` + enqueue async `mode="snapshot"` when `snapshot_jpeg` is empty!

- [ ] **Step 3: Run tests to verify failure**

Run: `go test ./internal/evidence/... ./internal/ingest/... -run "TestRecorderSnapshotFallback|TestProcessorSOP"`
Expected: FAIL.

- [ ] **Step 4: Implement changes in `evidence/recorder.go`, `evidence/postgres_repo.go`, `ingest/processor.go`, and `ingest/postgres_repo.go`**

- `evidence`: add `SnapshotGenerator` interface (implemented on `FFmpegClipGenerator` using `ffmpeg -y -rtsp_transport tcp -i <streamURL> -frames:v 1 -q:v 3 <out.jpg>`), add `UpdateAlarmImageEvidence` to `VideoRepo` & `PostgresVideoRepo`, dispatch on `req.Mode == "snapshot"` in `Recorder.HandleMessage`.
- `ingest/processor.go`: include `SOPProgress` in `wsFrameMessage`; persist `eval.CompletedSOPCycles`; pass `SOPTrace` + `ViolationType` into `PersistedAlarm`; when `imageKey == ""` and `p.mq != nil`, enqueue `evidenceRecordJob{Mode: "snapshot", AlarmID, CameraID, TriggeredAt}` so `aegis-dispatcher` grabs a single frame asynchronously.
- `ingest/postgres_repo.go`:
  - In `CreateAlarmIdempotent`: pass `SopTrace` into `CreateAlarm`, and inside the same `WithTx`, if `alarm.RuleMode == domain.RuleModeSOPSequence`, insert a `sop_cycles` row with `outcome='violated'` and `alarm_id=alarm.ID`.
  - Implement `RecordCompliantSOPCycle`.
  - In `ReloadRules`: load all `arming_schedules` (parsed via `rules.ParseArmingSchedule`) and populate both `rule.Schedule` and `rule.CameraROIs` (map of all enabled ROIs on that camera) so multi-ROI SOP steps and schedules work seamlessly on hot-reload!

- [ ] **Step 5: Run tests & commit**

Run: `go test -count=1 ./internal/evidence/... ./internal/ingest/...`
Expected: PASS. Commit `feat(sop): wire ingest sop_progress/sop_cycles persistence and async snapshot fallback`.

---

### Task 4: Analytics Summary & CSV Export Backend (`handlers_analytics.go`)

**Files:**
- Create: `backend/internal/platform/postgres/queries/analytics.sql`
- Create: `backend/internal/api/handlers_analytics.go`
- Create: `backend/internal/api/handlers_analytics_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Produces (`api`):
  - `GET /api/v1/analytics/summary?start_time=&end_time=&zone_id=&granularity=day|hour&shift=all|day|night&tz=Asia/Shanghai` (all roles)
    Returns `{ kpis, trend, by_zone, top_sop_steps, algo_precision }`.
  - `GET /api/v1/analytics/export?...` (all roles)
    Streams UTF-8 BOM CSV with formula-injection neutralization containing trend, zone comparison, top SOP violated steps, and algorithm version FP rates.

- [ ] **Step 1: Add `analytics.sql` and run `sqlc generate`**

Queries (all parameterized with `start_time`, `end_time`, optional `zone_ids uuid[]`, `tz`, and `shift` hour filtering `day: 08..19`, `night: 20..23 or 00..07`):
- `GetAnalyticsTrendByDay` & `GetAnalyticsTrendByHour`
- `GetAnalyticsByZone` (zone_id, zone_name, safety_count, sop_count, confirmed_count, false_positive_count)
- `GetAnalyticsSOPSummary` (`compliant_count`, `violated_count`, `avg_duration_ms` from `sop_cycles`)
- `GetAnalyticsTopSOPViolatedSteps` (groups `alarms` where `rule_mode = 'sop_sequence'` and `sop_trace IS NOT NULL` by `COALESCE(NULLIF(sop_trace->>'violated_step_name', ''), sop_trace->>'violation_type', 'unknown')`)
- `GetAnalyticsAlgoPrecision` (joins `algorithm_versions` + `algorithms` + `alarms` for `total_alarms`, `confirmed_count`, `false_positive_count`).

- [ ] **Step 2: Write failing integration test in `backend/internal/api/handlers_analytics_test.go`**

Test:
1. Review Focus #5: Empty future range returns `200`, finite `0.0` rates, non-null arrays.
2. Seeded zone + safety alarms (confirmed + false_positive) + SOP alarm with `sop_trace` + 3 compliant / 1 violated `sop_cycles` -> verifies `sop_compliance_rate == 75.0`, `top_sop_steps` contains violated step name, `algo_precision` computes accurate `fp_rate`, and `shift=day|night` + `granularity=hour` work cleanly.
3. Review Focus #5: `GET /api/v1/analytics/export` returns `text/csv` with UTF-8 BOM and neutralizes `=FORMULA` zone/step names.

- [ ] **Step 3: Implement `handlers_analytics.go`, mount routes in `router.go`, and verify tests pass**

Run: `go test -count=1 ./internal/api/... -run TestAnalytics`
Expected: PASS. Commit `feat(api): add enterprise analytics summary and csv export endpoints`.

---

### Task 5: Python Mock Runtime `sop` Mode & Live E2E SOP Verification

**Files:**
- Modify: `runtime/python-sdk/mock_runtime/main.py`
- Modify: `runtime/python-sdk/tests/test_runtime_lease_and_tasks.py`
- Modify: `backend/tests/e2e/e2e_pipeline_test.go`

**Interfaces:**
- Produces (`mock_runtime`): `algo_params["mock_mode"] = "sop"` with `sop_script`:
  - `"compliant"`: emits `wear_esd_strap` (2 frames) -> `scan_barcode` (2 frames) -> empty pause;
  - `"out_of_order"`: emits `scan_barcode` (2 frames) skipping `wear_esd_strap`;
  - `"timeout"`: emits `wear_esd_strap` (2 frames) then empty frames until timeout.

- [ ] **Step 1: Add failing pytest test for `MockAlgorithm` `sop` mode in `test_runtime_lease_and_tasks.py`**

Verify `sop_script="compliant"` emits `wear_esd_strap` followed by `scan_barcode`, and `sop_script="out_of_order"` emits `scan_barcode` immediately.

- [ ] **Step 2: Implement `sop` mode in `runtime/python-sdk/mock_runtime/main.py` and run pytest**

Run: `python -m pytest runtime/python-sdk/tests -v`
Expected: PASS.

- [ ] **Step 3: Add `TestE2ESOPSequencePipeline` to `backend/tests/e2e/e2e_pipeline_test.go`**

Spin up real Python Mock Runtime (`mock_mode="sop"`, `sop_script="out_of_order"`), register a `sop_sequence` rule with an active 24×7 arming schedule, verify:
- Real-time WS receives `frame_detections` containing non-empty `sop_progress`;
- `alarms` row is created with `rule_mode="sop_sequence"` and non-null `sop_trace`;
- `sop_cycles` row is created with `outcome="violated"`;
- `GET /api/v1/analytics/summary` reflects the violation!

- [ ] **Step 4: Run live VM E2E test & commit**

Run: `go test -tags=e2e -v -count=1 ./tests/e2e/...`
Expected: PASS. Commit `feat(e2e): add mock runtime sop simulation mode and live sop_sequence e2e test`.

---

### Task 6: Frontend Arming Schedules (`/schedules`) & Camera Config Workbench Upgrade (`/cameras/:id/config`)

**Files:**
- Modify: `web/locales/zh-CN.yaml`
- Modify: `web/locales/en.yaml`
- Modify: `web/src/api/aegis.ts`
- Modify: `web/src/router/modules/aegis.ts`
- Create: `web/src/views/schedules/index.vue`
- Modify: `web/src/components/aegis/RoiEditor.vue`
- Modify: `web/src/views/cameras/config.vue`
- Create: `web/src/views/schedules/__tests__/schedules.spec.ts`

**Interfaces:**
- Consumes: `/api/v1/schedules`, `/api/v1/cameras/:id/rules` (`schedule_id`, `sop_sequence` params).
- Produces:
  - Routes `/schedules/index` (admin) and `/analytics/index` (all roles) registered in `aegis.ts`.
  - `RoiEditor.vue` accepts optional `existingRois?: CameraROIItem[]` and `strokeColor?: string` to display all configured camera ROIs in their distinct colors alongside the active draft polygon.
  - `cameras/config.vue` supports creating & editing all 4 rule modes including the visual `sop_sequence` ordered step builder (add/remove/move-up/move-down steps, per-step ROI binding, required toggle, dwell frames, max wait seconds) + arming schedule binding.

- [ ] **Step 1: Add M2 i18n keys (`zh-CN.yaml`, `en.yaml`), TypeScript types & API functions (`aegis.ts`), and menu routes (`router/modules/aegis.ts`)**

- [ ] **Step 2: Create `web/src/views/schedules/index.vue` and unit test `schedules.spec.ts`**

Build schedule list + create/edit modal with interactive weekly window rows (Mon–Sun checkbox group + start/end `HH:MM` time inputs, overnight hint), `exclude_dates` and `include_dates` tag/row editors, and `409` delete protection feedback. Verify with `schedules.spec.ts`.

- [ ] **Step 3: Upgrade `RoiEditor.vue` (multi-ROI color display) and `cameras/config.vue` (freeze-frame helper, ROI color picker, rule edit, schedule binding, and visual `sop_sequence` step orchestrator)**

- [ ] **Step 4: Verify frontend typecheck & unit tests, then commit**

Run: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit`
Expected: PASS. Commit `feat(web): add arming schedules page and upgrade camera config with visual sop_sequence orchestrator`.

---

### Task 7: Monitor Wall Live SOP Step Progress Bar & Alarm Detail SOP Violation Timeline

**Files:**
- Modify: `web/src/views/monitor/index.vue`
- Modify: `web/src/views/monitor/__tests__/monitor.spec.ts`
- Modify: `web/src/views/alarms/index.vue`

**Interfaces:**
- Consumes: WS `frame_detections.sop_progress` (`SOPRuleProgress[]`) and `AlarmItem.sop_trace` (`SOPTrace | null`).

- [ ] **Step 1: Extend `monitor.spec.ts` with failing test for SOP progress bar rendering**

Simulate a WS `frame_detections` event carrying `sop_progress` (`step_1: completed`, `step_2: active`, `step_3: pending`) and assert the video tile renders `[data-test="sop-progress-bar"]` with step names and status styling.

- [ ] **Step 2: Upgrade `web/src/views/monitor/index.vue` to store and render `sop_progress`**

Store `sopProgress` on `liveFrames[camera_id]`; render a compact dark translucent SOP step progress bar along the bottom of the video tile whenever `(liveFrames[camId]?.sopProgress?.length ?? 0) > 0`.

- [ ] **Step 3: Upgrade `web/src/views/alarms/index.vue` detail drawer with SOP Violation Timeline**

When `selectedAlarm.rule_mode === 'sop_sequence' && selectedAlarm.sop_trace`, render a dedicated `el-timeline` card showing violation type badge, total duration, and each step's status (`completed`/`violated`/`skipped`/`pending`), elapsed time, and completion timestamp.

- [ ] **Step 4: Run frontend typecheck & unit tests, then commit**

Run: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit`
Expected: PASS. Commit `feat(web): add live sop progress bar on monitor wall and sop timeline in alarm detail`.

---

### Task 8: Enterprise Analytics Report Center (`/analytics`) & Full M2 Verification

**Files:**
- Create: `web/src/views/analytics/index.vue`
- Create: `web/src/views/analytics/__tests__/analytics.spec.ts`

**Interfaces:**
- Consumes: `getAnalyticsSummary(params)` and `exportAnalyticsCSVBlob(params)` from `web/src/api/aegis.ts`.

- [ ] **Step 1: Write failing unit test `web/src/views/analytics/__tests__/analytics.spec.ts`**

Mock `getAnalyticsSummary` and `getZonesTree`; mount `AnalyticsView`; assert KPI numbers (`sop_compliance_rate`, `avg_sop_duration_ms`), algorithm version FP table rows, and CSV export trigger.

- [ ] **Step 2: Implement `web/src/views/analytics/index.vue`**

Build filter bar (Quick Range: Today / 7d / 30d / Custom; Zone dropdown; Shift: All / Day / Night; Granularity: Day / Hour; Export CSV button), 4 summary cards, 3 offline ECharts panels (Safety vs SOP Trend, Zone Comparison, Top SOP Violated Steps), and Algorithm Version Precision/FP Rate table.

- [ ] **Step 3: Run complete M2 verification suite across Backend, Frontend, and Python SDK**

Run:
1. Backend: `golangci-lint run ./...`, `golangci-lint run --build-tags=e2e ./tests/e2e/...`, `go test -count=1 ./...`, `go test -tags=e2e -v -count=1 ./tests/e2e/...`, `CGO_ENABLED=0 go build ./...`.
2. Frontend: `pnpm.cmd --dir web typecheck && pnpm.cmd --dir web test:unit && pnpm.cmd --dir web build` + recursive zero-CDN check on `web/dist/`.
3. Python SDK: `python -m pytest runtime/python-sdk/tests -v`.
Expected: ALL PASS with 0 errors and 0 external CDN URLs.

- [ ] **Step 4: Commit & push `feat/enterprise-m2` for whole-branch review**

Commit `feat(web): add enterprise analytics report center` and push `feat/enterprise-m2`, then run an independent whole-branch review before merging.
