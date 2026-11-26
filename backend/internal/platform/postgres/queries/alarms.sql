-- name: CreateAlarm :one
INSERT INTO alarms (
    id, event_dedup_key, camera_id, roi_id, rule_id, algorithm_version_id,
    rule_mode, title, severity, triggered_at, detected_count,
    detections_snapshot, roi_snapshot, image_object_key, video_object_key, video_status
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: GetAlarmByID :one
SELECT * FROM alarms WHERE id = $1;

-- name: GetAlarmByDedupKey :one
SELECT * FROM alarms WHERE event_dedup_key = $1;

-- name: ListAlarms :many
SELECT * FROM alarms
WHERE (sqlc.narg('camera_id')::uuid IS NULL OR camera_id = sqlc.narg('camera_id'))
  AND (sqlc.narg('rule_mode')::varchar IS NULL OR rule_mode = sqlc.narg('rule_mode'))
  AND (sqlc.narg('handle_status')::varchar IS NULL OR handle_status = sqlc.narg('handle_status'))
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR triggered_at <= sqlc.narg('end_time'))
ORDER BY triggered_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountAlarms :one
SELECT COUNT(*) FROM alarms
WHERE (sqlc.narg('camera_id')::uuid IS NULL OR camera_id = sqlc.narg('camera_id'))
  AND (sqlc.narg('rule_mode')::varchar IS NULL OR rule_mode = sqlc.narg('rule_mode'))
  AND (sqlc.narg('handle_status')::varchar IS NULL OR handle_status = sqlc.narg('handle_status'))
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR triggered_at <= sqlc.narg('end_time'));

-- name: HandleAlarm :one
UPDATE alarms
SET handle_status = $2,
    handled_by = $3,
    handled_at = NOW(),
    handle_note = $4
WHERE id = $1
RETURNING *;

-- name: UpdateAlarmVideoEvidence :exec
UPDATE alarms
SET video_object_key = $2,
    video_status = $3
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
