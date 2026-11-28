package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	platformredis "aegisvision/backend/internal/platform/redis"
)

// Command types delivered from aegis-scheduler to aegis-ingest via RabbitMQ aegis.control.
const (
	CmdAssignTask = "assign_task"
	CmdUpdateTask = "update_task"
	CmdRevokeTask = "revoke_task"
	CmdRenewLease = "renew_lease"
)

// AgentCommandEnvelope wraps a targeted downstream command for an online aegis-agent node.
type AgentCommandEnvelope struct {
	CommandID   string               `json:"command_id"`
	NodeID      string               `json:"node_id"`
	CommandType string               `json:"command_type"`
	AssignTask  *AssignTaskPayload   `json:"assign_task,omitempty"`
	UpdateTask  *UpdateTaskPayload   `json:"update_task,omitempty"`
	RevokeTask  *RevokeTaskPayload   `json:"revoke_task,omitempty"`
	RenewLease  *RenewLeasePayload   `json:"renew_lease,omitempty"`
	IssuedAt    time.Time            `json:"issued_at"`
}

// CommandROI represents a normalized ROI passed to Agent/Runtime, including per-ROI target behavior labels.
type CommandROI struct {
	ROIID        string          `json:"roi_id"`
	Name         string          `json:"name"`
	ROIType      string          `json:"roi_type"`
	Points       json.RawMessage `json:"points"`
	TargetLabels []string        `json:"target_labels,omitempty"`
}

// AssignTaskPayload mirrors agent.proto AssignTaskCommand for MQ transport.
type AssignTaskPayload struct {
	TaskID             string       `json:"task_id"`
	CameraID           string       `json:"camera_id"`
	AlgorithmVersionID string       `json:"algorithm_version_id"`
	RuntimeKey         string       `json:"runtime_key"`
	StreamURL          string       `json:"stream_url"`
	StreamProtocol     string       `json:"stream_protocol"`
	ROIs               []CommandROI `json:"rois"`
	AlgoParamsJSON     string       `json:"algo_params_json"`
	LeaseID            string       `json:"lease_id"`
	LeaseTTLSeconds    int32        `json:"lease_ttl_seconds"`
	ConfigVersion      int64        `json:"config_version"`
}

// UpdateTaskPayload mirrors agent.proto UpdateTaskCommand.
type UpdateTaskPayload struct {
	TaskID         string       `json:"task_id"`
	ROIs           []CommandROI `json:"rois"`
	AlgoParamsJSON string       `json:"algo_params_json"`
	ConfigVersion  int64        `json:"config_version"`
}

// RevokeTaskPayload mirrors agent.proto RevokeTaskCommand.
type RevokeTaskPayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

// RenewLeasePayload mirrors agent.proto RenewLeaseCommand.
type RenewLeasePayload struct {
	TaskID             string `json:"task_id"`
	LeaseID            string `json:"lease_id"`
	LeaseTTLSeconds    int32  `json:"lease_ttl_seconds"`
	RefreshedStreamURL string `json:"refreshed_stream_url"`
}

func buildCommandROIs(rois []ROIInfo) []CommandROI {
	out := make([]CommandROI, 0, len(rois))
	for _, r := range rois {
		pts := json.RawMessage(r.Points)
		if len(pts) == 0 {
			pts = json.RawMessage(`[]`)
		}
		out = append(out, CommandROI{
			ROIID:        r.ID.String(),
			Name:         r.Name,
			ROIType:      r.ROIType,
			Points:       pts,
			TargetLabels: append([]string(nil), r.TargetLabels...),
		})
	}
	return out
}

func (r *Reconciler) issuePullStreamURL(ctx context.Context, taskID uuid.UUID, streamPath string) (string, error) {
	tok, err := auth.GenerateOpaqueToken()
	if err != nil {
		return "", fmt.Errorf("generate pull token: %w", err)
	}
	// Pull token TTL = leaseTTL + 30s per docs/design/02-protocols-and-apis.md section 6.2.
	pullTTL := r.leaseTTL + 30*time.Second
	if err := r.redis.SetStreamToken(ctx, platformredis.StreamTokenKindPull, tok, platformredis.StreamTokenPayload{
		TaskID:     taskID.String(),
		StreamPath: streamPath,
	}, pullTTL); err != nil {
		return "", fmt.Errorf("store stream pull token: %w", err)
	}
	return r.mediamtx.BuildAuthorizedRTSPURL(streamPath, tok), nil
}

func (r *Reconciler) publishCommand(ctx context.Context, ingestInstanceID string, env AgentCommandEnvelope) error {
	if r.bus == nil || ingestInstanceID == "" {
		return nil
	}
	if env.CommandID == "" {
		env.CommandID = uuid.Must(uuid.NewV7()).String()
	}
	if env.IssuedAt.IsZero() {
		env.IssuedAt = r.now()
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal agent command: %w", err)
	}
	return r.bus.PublishAgentCommand(ctx, ingestInstanceID, raw)
}
