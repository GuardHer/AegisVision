-- name: CreateMonitorPreset :one
INSERT INTO monitor_presets (id, owner_user_id, name, is_shared, grid_size, camera_ids, tour_interval_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetMonitorPresetByID :one
SELECT * FROM monitor_presets WHERE id = $1;

-- name: ListMonitorPresetsForUser :many
SELECT * FROM monitor_presets
WHERE is_shared = TRUE OR owner_user_id = $1
ORDER BY is_shared DESC, created_at DESC;

-- name: UpdateMonitorPreset :one
UPDATE monitor_presets
SET name = $2,
    is_shared = $3,
    grid_size = $4,
    camera_ids = $5,
    tour_interval_seconds = $6,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteMonitorPreset :exec
DELETE FROM monitor_presets WHERE id = $1;
