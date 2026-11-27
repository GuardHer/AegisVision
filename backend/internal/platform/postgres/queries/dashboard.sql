-- name: GetDashboardAlarmKPIs :one
SELECT
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('today_start') AND triggered_at < sqlc.arg('tomorrow_start'))::bigint AS today_alarms,
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('yesterday_start') AND triggered_at < sqlc.arg('today_start'))::bigint AS yesterday_alarms,
    COUNT(*) FILTER (WHERE handle_status = 'unhandled')::bigint AS unhandled_alarms,
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('today_start') AND triggered_at < sqlc.arg('tomorrow_start') AND handle_status = 'confirmed')::bigint AS today_confirmed,
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('today_start') AND triggered_at < sqlc.arg('tomorrow_start') AND handle_status = 'false_positive')::bigint AS today_false_positive,
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('today_start') AND triggered_at < sqlc.arg('tomorrow_start') AND handle_status IN ('confirmed', 'false_positive', 'ignored'))::bigint AS today_handled,
    COUNT(*) FILTER (WHERE triggered_at >= sqlc.arg('today_start') AND triggered_at < sqlc.arg('tomorrow_start') AND rule_mode = 'sop_sequence')::bigint AS today_sop_violations
FROM alarms;

-- name: GetDashboardSOPCycleCounts :one
SELECT
    COUNT(*) FILTER (WHERE outcome = 'compliant')::bigint AS compliant_count,
    COUNT(*) FILTER (WHERE outcome = 'violated')::bigint AS violated_count
FROM sop_cycles
WHERE ended_at >= sqlc.arg('since_time');

-- name: GetDashboardCameraHealth :one
SELECT
    COUNT(*)::bigint AS total_count,
    COUNT(*) FILTER (WHERE status = 'online')::bigint AS online_count
FROM cameras
WHERE enabled = TRUE;

-- name: GetDashboardNodeHealth :one
SELECT
    COUNT(*)::bigint AS total_nodes,
    COUNT(*) FILTER (WHERE status = 'online')::bigint AS online_nodes,
    COALESCE(SUM(running_tasks) FILTER (WHERE status = 'online'), 0)::bigint AS running_tasks
FROM agent_nodes;

-- name: GetAlarmTrendByDay :many
SELECT
    to_char((triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date, 'YYYY-MM-DD')::text AS day,
    COUNT(*) FILTER (WHERE rule_mode <> 'sop_sequence')::bigint AS safety_count,
    COUNT(*) FILTER (WHERE rule_mode = 'sop_sequence')::bigint AS sop_count
FROM alarms
WHERE triggered_at >= sqlc.arg('since_time')
GROUP BY (triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date
ORDER BY (triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date ASC;

-- name: GetTopZonesByAlarms :many
SELECT
    a.zone_id,
    COALESCE(z.name, 'Unassigned')::text AS zone_name,
    COUNT(*)::bigint AS alarm_count
FROM alarms a
LEFT JOIN zones z ON z.id = a.zone_id
WHERE a.triggered_at >= sqlc.arg('since_time')
GROUP BY a.zone_id, z.name
ORDER BY alarm_count DESC
LIMIT 10;

-- name: GetAlarmsByRuleMode :many
SELECT
    rule_mode,
    COUNT(*)::bigint AS count
FROM alarms
WHERE triggered_at >= sqlc.arg('since_time')
GROUP BY rule_mode
ORDER BY count DESC;

-- name: ListRecentHighSeverityAlarms :many
SELECT
    a.id,
    a.camera_id,
    COALESCE(c.name, '')::text AS camera_name,
    a.zone_id,
    COALESCE(z.name, '')::text AS zone_name,
    a.rule_mode,
    a.title,
    a.severity,
    a.handle_status,
    a.image_object_key,
    a.triggered_at
FROM alarms a
LEFT JOIN cameras c ON c.id = a.camera_id
LEFT JOIN zones z ON z.id = a.zone_id
WHERE a.handle_status = 'unhandled'
  AND a.severity IN ('critical', 'warning')
ORDER BY a.triggered_at DESC
LIMIT 10;
