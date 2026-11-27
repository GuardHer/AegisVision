-- name: CreateSOPCycle :one
INSERT INTO sop_cycles (
    id, rule_id, camera_id, zone_id, algorithm_version_id,
    outcome, violation_type, alarm_id, started_at, ended_at, duration_ms
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;
