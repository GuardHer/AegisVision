-- name: GetAnalyticsAlarmKPIs :one
SELECT
    COUNT(*) FILTER (WHERE a.rule_mode <> 'sop_sequence')::bigint AS safety_count,
    COUNT(*) FILTER (WHERE a.rule_mode = 'sop_sequence')::bigint AS sop_count,
    COUNT(*) FILTER (WHERE a.handle_status = 'confirmed')::bigint AS confirmed_count,
    COUNT(*) FILTER (WHERE a.handle_status = 'false_positive')::bigint AS false_positive_count,
    COUNT(*) FILTER (WHERE a.handle_status IN ('confirmed', 'false_positive', 'ignored'))::bigint AS handled_count
FROM alarms a
WHERE a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  );

-- name: GetAnalyticsTrendByDay :many
SELECT
    to_char((a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date, 'YYYY-MM-DD')::text AS bucket,
    COUNT(*) FILTER (WHERE a.rule_mode <> 'sop_sequence')::bigint AS safety_count,
    COUNT(*) FILTER (WHERE a.rule_mode = 'sop_sequence')::bigint AS sop_count
FROM alarms a
WHERE a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  )
GROUP BY (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date
ORDER BY (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)::date ASC;

-- name: GetAnalyticsTrendByHour :many
SELECT
    to_char(date_trunc('hour', a.triggered_at AT TIME ZONE sqlc.arg('tz')::text), 'YYYY-MM-DD HH24:00')::text AS bucket,
    COUNT(*) FILTER (WHERE a.rule_mode <> 'sop_sequence')::bigint AS safety_count,
    COUNT(*) FILTER (WHERE a.rule_mode = 'sop_sequence')::bigint AS sop_count
FROM alarms a
WHERE a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  )
GROUP BY date_trunc('hour', a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)
ORDER BY date_trunc('hour', a.triggered_at AT TIME ZONE sqlc.arg('tz')::text) ASC;

-- name: GetAnalyticsByZone :many
SELECT
    a.zone_id,
    COALESCE(z.name, 'Unassigned')::text AS zone_name,
    COUNT(*) FILTER (WHERE a.rule_mode <> 'sop_sequence')::bigint AS safety_count,
    COUNT(*) FILTER (WHERE a.rule_mode = 'sop_sequence')::bigint AS sop_count,
    COUNT(*) FILTER (WHERE a.handle_status = 'confirmed')::bigint AS confirmed_count,
    COUNT(*) FILTER (WHERE a.handle_status = 'false_positive')::bigint AS false_positive_count
FROM alarms a
LEFT JOIN zones z ON z.id = a.zone_id
WHERE a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  )
GROUP BY a.zone_id, z.name
ORDER BY (COUNT(*)) DESC
LIMIT 20;

-- name: GetAnalyticsSOPSummary :one
SELECT
    COUNT(*) FILTER (WHERE sc.outcome = 'compliant')::bigint AS compliant_count,
    COUNT(*) FILTER (WHERE sc.outcome = 'violated')::bigint AS violated_count,
    COALESCE(AVG(sc.duration_ms), 0)::bigint AS avg_duration_ms
FROM sop_cycles sc
WHERE sc.ended_at >= sqlc.arg('start_time')
  AND sc.ended_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR sc.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (sc.ended_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (sc.ended_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (sc.ended_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (sc.ended_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  );

-- name: GetAnalyticsTopSOPViolatedSteps :many
SELECT
    COALESCE(NULLIF(a.sop_trace->>'violated_step_name', ''), NULLIF(a.sop_trace->>'violation_type', ''), 'unknown')::text AS step_name,
    COALESCE(a.sop_trace->>'violation_type', '')::text AS violation_type,
    COUNT(*)::bigint AS count
FROM alarms a
WHERE a.rule_mode = 'sop_sequence'
  AND a.sop_trace IS NOT NULL
  AND a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  )
GROUP BY 1, 2
ORDER BY count DESC
LIMIT 15;

-- name: GetAnalyticsAlgoPrecision :many
SELECT
    v.id AS algorithm_version_id,
    ag.name::text AS algorithm_name,
    v.version::text AS version,
    COUNT(a.id)::bigint AS total_alarms,
    COUNT(a.id) FILTER (WHERE a.handle_status = 'confirmed')::bigint AS confirmed_count,
    COUNT(a.id) FILTER (WHERE a.handle_status = 'false_positive')::bigint AS false_positive_count,
    COUNT(a.id) FILTER (WHERE a.handle_status IN ('confirmed', 'false_positive', 'ignored'))::bigint AS handled_count
FROM alarms a
JOIN algorithm_versions v ON v.id = a.algorithm_version_id
JOIN algorithms ag ON ag.id = v.algorithm_id
WHERE a.triggered_at >= sqlc.arg('start_time')
  AND a.triggered_at < sqlc.arg('end_time')
  AND (sqlc.narg('zone_ids')::uuid[] IS NULL OR a.zone_id = ANY(sqlc.narg('zone_ids')::uuid[]))
  AND (
      sqlc.arg('shift')::text = 'all'
      OR (sqlc.arg('shift')::text = 'day' AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 8 AND EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 20)
      OR (sqlc.arg('shift')::text = 'night' AND (EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) >= 20 OR EXTRACT(HOUR FROM (a.triggered_at AT TIME ZONE sqlc.arg('tz')::text)) < 8))
  )
GROUP BY v.id, ag.name, v.version
ORDER BY total_alarms DESC;
