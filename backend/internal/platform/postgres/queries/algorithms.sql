-- name: CreateAlgorithm :one
INSERT INTO algorithms (id, algorithm_key, name, description, supported_labels)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetAlgorithmByID :one
SELECT * FROM algorithms WHERE id = $1;

-- name: GetAlgorithmByKey :one
SELECT * FROM algorithms WHERE algorithm_key = $1;

-- name: ListAlgorithms :many
SELECT * FROM algorithms ORDER BY created_at DESC;

-- name: UpdateAlgorithm :one
UPDATE algorithms
SET name = $2,
    description = $3,
    supported_labels = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteAlgorithm :exec
DELETE FROM algorithms WHERE id = $1;

-- name: CreateAlgorithmVersion :one
INSERT INTO algorithm_versions (id, algorithm_id, version, runtime_key, launch_spec, default_params, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAlgorithmVersionByID :one
SELECT * FROM algorithm_versions WHERE id = $1;

-- name: ListAlgorithmVersionsByAlgorithm :many
SELECT * FROM algorithm_versions
WHERE algorithm_id = $1
ORDER BY created_at DESC;

-- name: ListAllAlgorithmVersions :many
SELECT * FROM algorithm_versions ORDER BY created_at DESC;

-- name: UpdateAlgorithmVersion :one
UPDATE algorithm_versions
SET runtime_key = $2,
    launch_spec = $3,
    default_params = $4,
    status = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
