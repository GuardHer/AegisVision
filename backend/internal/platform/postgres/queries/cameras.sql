-- name: CreateCamera :one
INSERT INTO cameras (id, name, location, rtsp_url, sub_stream_url, stream_path, codec_hint, status, enabled, zone_id, workstation_code, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb))
RETURNING *;

-- name: GetCameraByID :one
SELECT * FROM cameras WHERE id = $1;

-- name: GetCameraByStreamPath :one
SELECT * FROM cameras WHERE stream_path = $1;

-- name: ListCameras :many
SELECT * FROM cameras ORDER BY created_at DESC;

-- name: ListEnabledCameras :many
SELECT * FROM cameras WHERE enabled = TRUE ORDER BY created_at ASC;

-- name: UpdateCamera :one
UPDATE cameras
SET name = $2,
    location = $3,
    rtsp_url = $4,
    sub_stream_url = $5,
    codec_hint = $6,
    enabled = $7,
    zone_id = $8,
    workstation_code = $9,
    tags = COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateCameraStatus :exec
UPDATE cameras
SET status = $2,
    last_online_at = CASE WHEN $2 = 'online' THEN NOW() ELSE last_online_at END,
    updated_at = NOW()
WHERE id = $1;

-- name: BatchUpdateCamerasEnabled :execrows
UPDATE cameras
SET enabled = sqlc.arg('enabled'),
    updated_at = NOW()
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: BatchUpdateCamerasZone :execrows
UPDATE cameras
SET zone_id = sqlc.narg('zone_id'),
    updated_at = NOW()
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: DeleteCamera :exec
DELETE FROM cameras WHERE id = $1;

-- name: CreateCameraROI :one
INSERT INTO camera_rois (id, camera_id, name, roi_type, points, enabled, color)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF(sqlc.arg('color')::varchar, ''), '#38bdf8'))
RETURNING *;

-- name: GetCameraROIByID :one
SELECT * FROM camera_rois WHERE id = $1;

-- name: ListCameraROIsByCamera :many
SELECT * FROM camera_rois
WHERE camera_id = $1
ORDER BY created_at ASC;

-- name: UpdateCameraROI :one
UPDATE camera_rois
SET name = $2,
    roi_type = $3,
    points = $4,
    enabled = $5,
    color = COALESCE(NULLIF(sqlc.arg('color')::varchar, ''), '#38bdf8'),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteCameraROI :exec
DELETE FROM camera_rois WHERE id = $1;
