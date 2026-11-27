-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    id, actor_user_id, actor_username, action, resource_type, resource_id, summary, detail, client_ip, request_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListAuditLogs :many
SELECT * FROM audit_logs
WHERE (sqlc.narg('actor_username')::varchar IS NULL OR actor_username ILIKE '%' || sqlc.narg('actor_username')::text || '%')
  AND (sqlc.narg('resource_type')::varchar IS NULL OR resource_type = sqlc.narg('resource_type'))
  AND (sqlc.narg('action')::varchar IS NULL OR action ILIKE '%' || sqlc.narg('action')::text || '%')
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR created_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR created_at <= sqlc.narg('end_time'))
ORDER BY created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountAuditLogs :one
SELECT COUNT(*) FROM audit_logs
WHERE (sqlc.narg('actor_username')::varchar IS NULL OR actor_username ILIKE '%' || sqlc.narg('actor_username')::text || '%')
  AND (sqlc.narg('resource_type')::varchar IS NULL OR resource_type = sqlc.narg('resource_type'))
  AND (sqlc.narg('action')::varchar IS NULL OR action ILIKE '%' || sqlc.narg('action')::text || '%')
  AND (sqlc.narg('start_time')::timestamptz IS NULL OR created_at >= sqlc.narg('start_time'))
  AND (sqlc.narg('end_time')::timestamptz IS NULL OR created_at <= sqlc.narg('end_time'));

-- name: ListSystemSettings :many
SELECT * FROM system_settings ORDER BY key ASC;

-- name: UpsertSystemSetting :one
INSERT INTO system_settings (key, value, updated_by, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW()
RETURNING *;

-- name: ListAlgorithmVersionStats30d :many
SELECT
    av.id AS algorithm_version_id,
    COUNT(a.id)::bigint AS total_alarms,
    COUNT(a.id) FILTER (WHERE a.handle_status = 'confirmed')::bigint AS confirmed_count,
    COUNT(a.id) FILTER (WHERE a.handle_status = 'false_positive')::bigint AS false_positive_count,
    COUNT(a.id) FILTER (WHERE a.handle_status IN ('confirmed', 'false_positive', 'ignored'))::bigint AS handled_count
FROM algorithm_versions av
LEFT JOIN alarms a
    ON a.algorithm_version_id = av.id
   AND a.triggered_at >= NOW() - INTERVAL '30 days'
WHERE av.algorithm_id = $1
GROUP BY av.id;
