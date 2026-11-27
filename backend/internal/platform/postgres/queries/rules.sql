-- name: CreateROIAlgorithmRule :one
INSERT INTO roi_algorithm_rules (
    id, camera_id, roi_id, algorithm_version_id, name, rule_mode,
    params, algo_params_override, schedule_mode, pinned_node_id,
    record_video, video_pre_seconds, video_post_seconds, enabled, schedule_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: GetROIAlgorithmRuleByID :one
SELECT * FROM roi_algorithm_rules WHERE id = $1;

-- name: ListRulesByCamera :many
SELECT * FROM roi_algorithm_rules
WHERE camera_id = $1
ORDER BY created_at ASC;

-- name: ListEnabledRules :many
SELECT * FROM roi_algorithm_rules
WHERE enabled = TRUE
ORDER BY camera_id, created_at ASC;

-- name: UpdateROIAlgorithmRule :one
UPDATE roi_algorithm_rules
SET name = $2,
    rule_mode = $3,
    params = $4,
    algo_params_override = $5,
    schedule_mode = $6,
    pinned_node_id = $7,
    record_video = $8,
    video_pre_seconds = $9,
    video_post_seconds = $10,
    enabled = $11,
    schedule_id = $12,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteROIAlgorithmRule :exec
DELETE FROM roi_algorithm_rules WHERE id = $1;

-- name: CreateAlarmChannel :one
INSERT INTO alarm_channels (id, name, channel_type, config, routing_rules, enabled)
VALUES ($1, $2, $3, $4, COALESCE(sqlc.narg('routing_rules')::jsonb, '{"auto_route":false}'::jsonb), $5)
RETURNING *;

-- name: GetAlarmChannelByID :one
SELECT * FROM alarm_channels WHERE id = $1;

-- name: ListAlarmChannels :many
SELECT * FROM alarm_channels ORDER BY created_at DESC;

-- name: ListEnabledAlarmChannels :many
SELECT * FROM alarm_channels WHERE enabled = TRUE ORDER BY created_at ASC;

-- name: UpdateAlarmChannel :one
UPDATE alarm_channels
SET name = $2,
    channel_type = $3,
    config = $4,
    routing_rules = COALESCE(sqlc.narg('routing_rules')::jsonb, routing_rules),
    enabled = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteAlarmChannel :exec
DELETE FROM alarm_channels WHERE id = $1;

-- name: LinkRuleAlarmChannel :exec
INSERT INTO rule_alarm_channels (rule_id, channel_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ClearRuleAlarmChannels :exec
DELETE FROM rule_alarm_channels WHERE rule_id = $1;

-- name: ListChannelsByRule :many
SELECT c.*
FROM alarm_channels c
INNER JOIN rule_alarm_channels rac ON rac.channel_id = c.id
WHERE rac.rule_id = $1 AND c.enabled = TRUE
ORDER BY c.created_at ASC;

-- name: CountChannelRulesInZones :one
SELECT COUNT(*)::bigint
FROM rule_alarm_channels rac
JOIN roi_algorithm_rules r ON r.id = rac.rule_id
JOIN cameras c ON c.id = r.camera_id
WHERE rac.channel_id = $1
  AND c.zone_id = ANY(sqlc.arg('zone_ids')::uuid[]);
