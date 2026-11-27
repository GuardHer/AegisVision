-- name: CreateAlarm :one
INSERT INTO alarms (
    id, event_dedup_key, camera_id, zone_id, roi_id, rule_id, algorithm_version_id,
    rule_mode, title, severity, triggered_at, detected_count,
    detections_snapshot, roi_snapshot, image_object_key, video_object_key, video_status, sop_trace
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
RETURNING *;

-- name: GetAlarmByID :one
SELECT * FROM alarms WHERE id = $1;

-- name: GetAlarmByDedupKey :one
SELECT * FROM alarms WHERE event_dedup_key = $1;

-- name: ListAlarms :many
SELECT * FROM alarms
WHERE (sqlc.narg('camera_id')::uuid IS NULL OR camera_id = sqlc.narg('camera_id'))
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('rule_mode')::varchar IS NULL OR rule_mode = sqlc.narg('rule_mode'))
  AND (sqlc.narg('severity')::varchar IS NULL OR severity = sqlc.narg('severity'))
  AND (sqlc.narg('handle_status')::varchar IS NULL OR handle_status = sqlc.narg('handle_status'))
  AND (sqlc.narg('keyword')::text IS NULL OR title ILIKE '%' || sqlc.narg('keyword')::text || '%')
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR triggered_at <= sqlc.narg('end_time'))
ORDER BY triggered_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountAlarms :one
SELECT COUNT(*) FROM alarms
WHERE (sqlc.narg('camera_id')::uuid IS NULL OR camera_id = sqlc.narg('camera_id'))
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('rule_mode')::varchar IS NULL OR rule_mode = sqlc.narg('rule_mode'))
  AND (sqlc.narg('severity')::varchar IS NULL OR severity = sqlc.narg('severity'))
  AND (sqlc.narg('handle_status')::varchar IS NULL OR handle_status = sqlc.narg('handle_status'))
  AND (sqlc.narg('keyword')::text IS NULL OR title ILIKE '%' || sqlc.narg('keyword')::text || '%')
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR triggered_at <= sqlc.narg('end_time'));

-- name: ListAlarmsForExport :many
SELECT
    a.*,
    COALESCE(c.name, '')::text AS camera_name,
    COALESCE(z.name, '')::text AS zone_name
FROM alarms a
LEFT JOIN cameras c ON c.id = a.camera_id
LEFT JOIN zones z ON z.id = a.zone_id
WHERE (sqlc.narg('camera_id')::uuid IS NULL OR a.camera_id = sqlc.narg('camera_id'))
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('rule_mode')::varchar IS NULL OR a.rule_mode = sqlc.narg('rule_mode'))
  AND (sqlc.narg('severity')::varchar IS NULL OR a.severity = sqlc.narg('severity'))
  AND (sqlc.narg('handle_status')::varchar IS NULL OR a.handle_status = sqlc.narg('handle_status'))
  AND (sqlc.narg('keyword')::text IS NULL OR a.title ILIKE '%' || sqlc.narg('keyword')::text || '%')
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR a.triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR a.triggered_at <= sqlc.narg('end_time'))
ORDER BY a.triggered_at DESC
LIMIT sqlc.arg('max_rows');

-- name: HandleAlarm :one
UPDATE alarms
SET handle_status = $2,
    handled_by = $3,
    handled_at = NOW(),
    handle_note = $4,
    false_positive_reason = $5
WHERE id = $1
RETURNING *;

-- name: BatchHandleAlarms :execrows
UPDATE alarms
SET handle_status = sqlc.arg('handle_status'),
    handled_by = sqlc.narg('handled_by'),
    handled_at = NOW(),
    handle_note = sqlc.arg('handle_note'),
    false_positive_reason = sqlc.arg('false_positive_reason')
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: UpdateAlarmVideoEvidence :exec
UPDATE alarms
SET video_object_key = $2,
    video_status = $3
WHERE id = $1;

-- name: UpdateAlarmImageEvidence :exec
UPDATE alarms
SET image_object_key = $2
WHERE id = $1;

-- name: CreateAlarmDelivery :one
INSERT INTO alarm_deliveries (id, alarm_id, channel_id, channel_type, status, max_attempts)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAlarmDeliveryByID :one
SELECT * FROM alarm_deliveries WHERE id = $1;

-- name: ListDeliveriesByAlarm :many
SELECT * FROM alarm_deliveries
WHERE alarm_id = $1
ORDER BY created_at ASC;

-- name: UpdateAlarmDeliveryResult :one
UPDATE alarm_deliveries
SET status = $2,
    attempts = $3,
    last_status_code = $4,
    last_latency_ms = $5,
    last_error = $6,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
