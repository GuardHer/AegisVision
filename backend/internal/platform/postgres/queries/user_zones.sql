-- name: GetUserZoneScopeFlags :one
SELECT id, role, all_zones_access FROM users WHERE id = $1;

-- name: ListUserZoneIDs :many
SELECT zone_id FROM user_zones WHERE user_id = $1;

-- name: UpdateUserAllZonesAccess :exec
UPDATE users SET all_zones_access = $2, updated_at = NOW() WHERE id = $1;

-- name: DeleteUserZonesByUser :exec
DELETE FROM user_zones WHERE user_id = $1;

-- name: InsertUserZone :exec
INSERT INTO user_zones (user_id, zone_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;
