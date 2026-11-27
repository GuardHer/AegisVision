-- name: UpsertAgentNode :one
INSERT INTO agent_nodes (node_id, hostname, ip_addr, runtime_keys, max_tasks, running_tasks, status, meta, last_heartbeat_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
ON CONFLICT (node_id) DO UPDATE SET
    hostname = EXCLUDED.hostname,
    ip_addr = EXCLUDED.ip_addr,
    runtime_keys = EXCLUDED.runtime_keys,
    max_tasks = EXCLUDED.max_tasks,
    running_tasks = EXCLUDED.running_tasks,
    status = CASE WHEN agent_nodes.status = 'draining' THEN 'draining' ELSE EXCLUDED.status END,
    meta = EXCLUDED.meta,
    last_heartbeat_at = NOW(),
    updated_at = NOW()
RETURNING *;

-- name: GetAgentNodeByID :one
SELECT * FROM agent_nodes WHERE node_id = $1;

-- name: ListAgentNodes :many
SELECT * FROM agent_nodes ORDER BY node_id ASC;

-- name: UpdateAgentNodeStatus :one
UPDATE agent_nodes
SET status = $2,
    updated_at = NOW()
WHERE node_id = $1
RETURNING *;

-- name: UpdateAgentNodeHeartbeat :exec
UPDATE agent_nodes
SET running_tasks = $2,
    meta = $3,
    status = CASE WHEN status = 'offline' THEN 'online' ELSE status END,
    last_heartbeat_at = NOW(),
    updated_at = NOW()
WHERE node_id = $1;

-- name: CreateInferenceTask :one
INSERT INTO inference_tasks (
    id, camera_id, algorithm_version_id, runtime_key, desired_state, actual_state, config_version
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetInferenceTaskByID :one
SELECT * FROM inference_tasks WHERE id = $1;

-- name: ListInferenceTasks :many
SELECT * FROM inference_tasks ORDER BY created_at DESC;

-- name: ListRunningInferenceTasks :many
SELECT * FROM inference_tasks
WHERE desired_state = 'running'
ORDER BY created_at ASC;

-- name: UpdateInferenceTaskAssignment :one
UPDATE inference_tasks
SET assigned_node_id = $2,
    ingest_instance_id = $3,
    lease_id = $4,
    lease_expires_at = $5,
    config_version = $6,
    actual_state = $7,
    error_message = $8,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateInferenceTaskState :exec
UPDATE inference_tasks
SET actual_state = $2,
    error_message = $3,
    updated_at = NOW()
WHERE id = $1;

-- name: UpdateInferenceTaskLastFrame :exec
UPDATE inference_tasks
SET last_frame_at = $2,
    updated_at = NOW()
WHERE id = $1;

-- name: StopInferenceTask :exec
UPDATE inference_tasks
SET desired_state = 'stopped',
    actual_state = 'stopped',
    updated_at = NOW()
WHERE id = $1;

-- name: ResetInferenceTaskForReschedule :one
UPDATE inference_tasks
SET assigned_node_id = NULL,
    actual_state = 'pending',
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
