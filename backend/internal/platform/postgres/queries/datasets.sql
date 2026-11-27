-- name: ListFalsePositiveAlarms :many
SELECT
    a.*,
    COALESCE(c.name, '')::text AS camera_name,
    COALESCE(z.name, '')::text AS zone_name,
    COALESCE(alg.name, '')::text AS algorithm_name,
    COALESCE(av.version, '')::text AS algorithm_version
FROM alarms a
LEFT JOIN cameras c ON c.id = a.camera_id
LEFT JOIN zones z ON z.id = a.zone_id
LEFT JOIN algorithm_versions av ON av.id = a.algorithm_version_id
LEFT JOIN algorithms alg ON alg.id = av.algorithm_id
WHERE a.handle_status = 'false_positive'
  AND a.image_object_key <> ''
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('algorithm_version_id')::uuid IS NULL OR a.algorithm_version_id = sqlc.narg('algorithm_version_id'))
  AND (sqlc.narg('false_positive_reason')::varchar IS NULL OR a.false_positive_reason = sqlc.narg('false_positive_reason'))
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR a.triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR a.triggered_at <= sqlc.narg('end_time'))
ORDER BY a.triggered_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountFalsePositiveAlarms :one
SELECT COUNT(*)
FROM alarms a
WHERE a.handle_status = 'false_positive'
  AND a.image_object_key <> ''
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('algorithm_version_id')::uuid IS NULL OR a.algorithm_version_id = sqlc.narg('algorithm_version_id'))
  AND (sqlc.narg('false_positive_reason')::varchar IS NULL OR a.false_positive_reason = sqlc.narg('false_positive_reason'))
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR a.triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR a.triggered_at <= sqlc.narg('end_time'));

-- name: ListFalsePositiveAlarmsForExport :many
SELECT
    a.*,
    COALESCE(c.name, '')::text AS camera_name,
    COALESCE(z.name, '')::text AS zone_name,
    COALESCE(alg.name, '')::text AS algorithm_name,
    COALESCE(av.version, '')::text AS algorithm_version
FROM alarms a
LEFT JOIN cameras c ON c.id = a.camera_id
LEFT JOIN zones z ON z.id = a.zone_id
LEFT JOIN algorithm_versions av ON av.id = a.algorithm_version_id
LEFT JOIN algorithms alg ON alg.id = av.algorithm_id
WHERE a.handle_status = 'false_positive'
  AND a.image_object_key <> ''
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (sqlc.narg('algorithm_version_id')::uuid IS NULL OR a.algorithm_version_id = sqlc.narg('algorithm_version_id'))
  AND (sqlc.narg('false_positive_reason')::varchar IS NULL OR a.false_positive_reason = sqlc.narg('false_positive_reason'))
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR a.triggered_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR a.triggered_at <= sqlc.narg('end_time'))
ORDER BY a.triggered_at DESC
LIMIT sqlc.arg('max_rows');

-- name: GetSystemSettingByKey :one
SELECT * FROM system_settings WHERE key = $1;
