-- name: CreateArmingSchedule :one
INSERT INTO arming_schedules (
    id, name, description, timezone, weekly_windows, exclude_dates, include_dates, enabled
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetArmingScheduleByID :one
SELECT * FROM arming_schedules WHERE id = $1;

-- name: ListArmingSchedules :many
SELECT
    s.id,
    s.name,
    s.description,
    s.timezone,
    s.weekly_windows,
    s.exclude_dates,
    s.include_dates,
    s.enabled,
    s.created_at,
    s.updated_at,
    COUNT(r.id)::bigint AS rule_count
FROM arming_schedules s
LEFT JOIN roi_algorithm_rules r ON r.schedule_id = s.id
GROUP BY s.id
ORDER BY s.created_at DESC;

-- name: UpdateArmingSchedule :one
UPDATE arming_schedules
SET name = $2,
    description = $3,
    timezone = $4,
    weekly_windows = $5,
    exclude_dates = $6,
    include_dates = $7,
    enabled = $8,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteArmingSchedule :exec
DELETE FROM arming_schedules WHERE id = $1;

-- name: CountRulesUsingSchedule :one
SELECT COUNT(*)::bigint FROM roi_algorithm_rules WHERE schedule_id = $1;
