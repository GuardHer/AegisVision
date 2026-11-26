-- name: CreateCamera :one
INSERT INTO cameras (id, name, location, rtsp_url, sub_stream_url, stream_path, codec_hint, status, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateCameraStatus :exec
UPDATE cameras
SET status = $2,
    updated_at = NOW()
WHERE id = $1;

-- name: DeleteCamera :exec
DELETE FROM cameras WHERE id = $1;

-- name: CreateCameraROI :one
INSERT INTO camera_rois (id, camera_id, name, roi_type, points, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
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
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteCameraROI :exec
DELETE FROM camera_rois WHERE id = $1;
