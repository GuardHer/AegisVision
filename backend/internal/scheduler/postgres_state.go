package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// PostgresStateStore adapts postgres.Store to scheduler.StateStore.
type PostgresStateStore struct {
	store *postgres.Store
}

// NewPostgresStateStore constructs a PostgreSQL-backed StateStore for aegis-scheduler.
func NewPostgresStateStore(store *postgres.Store) *PostgresStateStore {
	return &PostgresStateStore{store: store}
}

// LoadSnapshot loads cameras, versions, ROIs, enabled rules, agent nodes, and running tasks.
func (p *PostgresStateStore) LoadSnapshot(ctx context.Context) (Snapshot, error) {
	q := p.store.Queries()

	cams, err := q.ListCameras(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list cameras: %w", err)
	}
	vers, err := q.ListAllAlgorithmVersions(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list algorithm versions: %w", err)
	}
	rules, err := q.ListEnabledRules(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list enabled rules: %w", err)
	}
	nodes, err := q.ListAgentNodes(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list agent nodes: %w", err)
	}
	tasks, err := q.ListRunningInferenceTasks(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list running inference tasks: %w", err)
	}

	snap := Snapshot{
		Cameras:  make([]CameraInfo, 0, len(cams)),
		Versions: make([]VersionInfo, 0, len(vers)),
		ROIs:     make([]ROIInfo, 0),
		Rules:    make([]RuleInfo, 0, len(rules)),
		Nodes:    make([]NodeInfo, 0, len(nodes)),
		Tasks:    make([]ExistingTaskInfo, 0, len(tasks)),
	}

	for _, c := range cams {
		snap.Cameras = append(snap.Cameras, CameraInfo{
			ID:           c.ID,
			StreamPath:   c.StreamPath,
			SourceURL:    c.RtspUrl,
			SubStreamURL: c.SubStreamUrl,
			Status:       c.Status,
			Enabled:      c.Enabled,
		})
		rois, roiErr := q.ListCameraROIsByCamera(ctx, c.ID)
		if roiErr != nil {
			return Snapshot{}, fmt.Errorf("list camera rois for %s: %w", c.ID, roiErr)
		}
		for _, r := range rois {
			snap.ROIs = append(snap.ROIs, ROIInfo{
				ID:       r.ID,
				CameraID: r.CameraID,
				Name:     r.Name,
				ROIType:  r.RoiType,
				Points:   r.Points,
				Enabled:  r.Enabled,
			})
		}
	}

	for _, v := range vers {
		snap.Versions = append(snap.Versions, VersionInfo{
			ID:            v.ID,
			AlgorithmID:   v.AlgorithmID,
			Version:       v.Version,
			RuntimeKey:    v.RuntimeKey,
			DefaultParams: v.DefaultParams,
			Status:        v.Status,
		})
	}

	for _, r := range rules {
		snap.Rules = append(snap.Rules, RuleInfo{
			ID:                 r.ID,
			CameraID:           r.CameraID,
			ROIID:              r.RoiID,
			AlgorithmVersionID: r.AlgorithmVersionID,
			ScheduleMode:       r.ScheduleMode,
			PinnedNodeID:       r.PinnedNodeID.String,
			AlgoParamsOverride: r.AlgoParamsOverride,
			RecordVideo:        r.RecordVideo,
			Enabled:            r.Enabled,
		})
	}

	for _, n := range nodes {
		var rtKeys []string
		_ = json.Unmarshal(n.RuntimeKeys, &rtKeys)
		var hb time.Time
		if n.LastHeartbeatAt != nil {
			hb = *n.LastHeartbeatAt
		}
		snap.Nodes = append(snap.Nodes, NodeInfo{
			NodeID:          n.NodeID,
			RuntimeKeys:     rtKeys,
			MaxTasks:        int(n.MaxTasks),
			RunningTasks:    int(n.RunningTasks),
			Status:          n.Status,
			LastHeartbeatAt: hb,
		})
	}

	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, ExistingTaskInfo{
			ID:                 t.ID,
			CameraID:           t.CameraID,
			AlgorithmVersionID: t.AlgorithmVersionID,
			RuntimeKey:         t.RuntimeKey,
			AssignedNodeID:     t.AssignedNodeID.String,
			IngestInstanceID:   t.IngestInstanceID,
			DesiredState:       t.DesiredState,
			ActualState:        t.ActualState,
			LeaseID:            t.LeaseID,
			LeaseExpiresAt:     t.LeaseExpiresAt,
			ConfigVersion:      t.ConfigVersion,
			ErrorMessage:       t.ErrorMessage,
		})
	}

	return snap, nil
}

// MarkNodeOffline updates an unresponsive node's status to offline.
func (p *PostgresStateStore) MarkNodeOffline(ctx context.Context, nodeID string) error {
	_, err := p.store.Queries().UpdateAgentNodeStatus(ctx, sqlcgen.UpdateAgentNodeStatusParams{
		NodeID: nodeID,
		Status: domain.NodeStatusOffline,
	})
	return err
}

// UpdateCameraStatus persists a camera's online/offline status (and updates last_online_at when online).
func (p *PostgresStateStore) UpdateCameraStatus(ctx context.Context, cameraID uuid.UUID, status string) error {
	return p.store.Queries().UpdateCameraStatus(ctx, sqlcgen.UpdateCameraStatusParams{
		ID:     cameraID,
		Status: status,
	})
}

// SaveTaskAssignment creates or updates an inference_tasks assignment row.
func (p *PostgresStateStore) SaveTaskAssignment(ctx context.Context, task ExistingTaskInfo) error {
	q := p.store.Queries()
	_, err := q.GetInferenceTaskByID(ctx, task.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, createErr := q.CreateInferenceTask(ctx, sqlcgen.CreateInferenceTaskParams{
			ID:                 task.ID,
			CameraID:           task.CameraID,
			AlgorithmVersionID: task.AlgorithmVersionID,
			RuntimeKey:         task.RuntimeKey,
			DesiredState:       task.DesiredState,
			ActualState:        task.ActualState,
			ConfigVersion:      task.ConfigVersion,
		}); createErr != nil {
			return fmt.Errorf("create inference task: %w", createErr)
		}
	} else if err != nil {
		return fmt.Errorf("lookup inference task: %w", err)
	}

	var assigned pgtype.Text
	if task.AssignedNodeID != "" {
		assigned = pgtype.Text{String: task.AssignedNodeID, Valid: true}
	}

	_, err = q.UpdateInferenceTaskAssignment(ctx, sqlcgen.UpdateInferenceTaskAssignmentParams{
		ID:               task.ID,
		AssignedNodeID:   assigned,
		IngestInstanceID: task.IngestInstanceID,
		LeaseID:          task.LeaseID,
		LeaseExpiresAt:   task.LeaseExpiresAt,
		ConfigVersion:    task.ConfigVersion,
		ActualState:      task.ActualState,
		ErrorMessage:     task.ErrorMessage,
	})
	return err
}

// StopTask marks an obsolete inference task as stopped.
func (p *PostgresStateStore) StopTask(ctx context.Context, taskID uuid.UUID) error {
	return p.store.Queries().StopInferenceTask(ctx, taskID)
}
