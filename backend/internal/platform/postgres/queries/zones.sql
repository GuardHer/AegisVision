-- name: CreateZone :one
INSERT INTO zones (id, parent_id, name, code, zone_type, sort_order, description)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetZoneByID :one
SELECT * FROM zones WHERE id = $1;

-- name: ListZones :many
SELECT * FROM zones
ORDER BY sort_order ASC, name ASC, created_at ASC;

-- name: UpdateZone :one
UPDATE zones
SET parent_id = $2,
    name = $3,
    code = $4,
    zone_type = $5,
    sort_order = $6,
    description = $7,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteZone :exec
DELETE FROM zones WHERE id = $1;

-- name: CountChildZones :one
SELECT COUNT(*) FROM zones WHERE parent_id = $1;

-- name: CountCamerasInZone :one
SELECT COUNT(*) FROM cameras WHERE zone_id = $1;

-- name: ListCameraCountsByZone :many
SELECT
    zone_id,
    COUNT(*)::bigint AS total_count,
    COUNT(*) FILTER (WHERE status = 'online')::bigint AS online_count
FROM cameras
WHERE zone_id IS NOT NULL
GROUP BY zone_id;
