package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/rules"
)

// PostgresRepo adapts postgres.Store to ingest.Repository and provides rule hot-reloading.
type PostgresRepo struct {
	store *postgres.Store
}

// NewPostgresRepo constructs a PostgreSQL-backed Repository for aegis-ingest.
func NewPostgresRepo(store *postgres.Store) *PostgresRepo {
	return &PostgresRepo{store: store}
}

// UpsertAgentNode registers or refreshes an online compute host.
func (r *PostgresRepo) UpsertAgentNode(ctx context.Context, node NodeRegistration) error {
	keysJSON, _ := json.Marshal(node.RuntimeKeys)
	if len(keysJSON) == 0 || string(keysJSON) == "null" {
		keysJSON = []byte(`[]`)
	}
	meta := node.Meta
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	maxTasks := int32(node.MaxTasks) // #nosec G115 -- task count is small positive int
	if maxTasks <= 0 {
		maxTasks = 8
	}
	_, err := r.store.Queries().UpsertAgentNode(ctx, sqlcgen.UpsertAgentNodeParams{
		NodeID:       node.NodeID,
		Hostname:     node.Hostname,
		IpAddr:       node.IPAddr,
		RuntimeKeys:  keysJSON,
		MaxTasks:     maxTasks,
		RunningTasks: 0,
		Status:       domain.NodeStatusOnline,
		Meta:         meta,
	})
	return err
}

// UpdateAgentHeartbeat records node heartbeat load metrics.
func (r *PostgresRepo) UpdateAgentHeartbeat(ctx context.Context, nodeID string, runningTasks int, meta []byte) error {
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	return r.store.Queries().UpdateAgentNodeHeartbeat(ctx, sqlcgen.UpdateAgentNodeHeartbeatParams{
		NodeID:       nodeID,
		RunningTasks: int32(runningTasks), // #nosec G115 -- task count
		Meta:         meta,
	})
}

// UpdateTaskState updates inference_tasks.actual_state and error_message.
func (r *PostgresRepo) UpdateTaskState(ctx context.Context, taskID, actualState, errMsg string) error {
	id, err := uuid.Parse(taskID)
	if err != nil {
		return nil
	}
	return r.store.Queries().UpdateInferenceTaskState(ctx, sqlcgen.UpdateInferenceTaskStateParams{
		ID:           id,
		ActualState:  actualState,
		ErrorMessage: errMsg,
	})
}

// UpdateTaskLastFrame updates inference_tasks.last_frame_at.
func (r *PostgresRepo) UpdateTaskLastFrame(ctx context.Context, taskID string, ts time.Time) error {
	id, err := uuid.Parse(taskID)
	if err != nil {
		return nil
	}
	return r.store.Queries().UpdateInferenceTaskLastFrame(ctx, sqlcgen.UpdateInferenceTaskLastFrameParams{
		ID:          id,
		LastFrameAt: &ts,
	})
}

// CreateAlarmIdempotent inserts an alarm and its initial alarm_deliveries inside a single transaction,
// returning created=false cleanly if event_dedup_key already exists.
func (r *PostgresRepo) CreateAlarmIdempotent(ctx context.Context, alarm PersistedAlarm) (bool, []AlarmChannelBinding, error) {
	var dedup pgtype.Text
	if alarm.EventDedupKey != "" {
		dedup = pgtype.Text{String: alarm.EventDedupKey, Valid: true}
		if _, err := r.store.Queries().GetAlarmByDedupKey(ctx, dedup); err == nil {
			return false, nil, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, nil, err
		}
	}

	var bindings []AlarmChannelBinding
	err := r.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
			ID:                 alarm.ID,
			EventDedupKey:      dedup,
			CameraID:           alarm.CameraID,
			RoiID:              alarm.ROIID,
			RuleID:             alarm.RuleID,
			AlgorithmVersionID: alarm.AlgorithmVersionID,
			RuleMode:           alarm.RuleMode,
			Title:              alarm.Title,
			Severity:           alarm.Severity,
			TriggeredAt:        alarm.TriggeredAt,
			DetectedCount:      int32(alarm.DetectedCount), // #nosec G115 -- detection count
			DetectionsSnapshot: alarm.DetectionsSnapshot,
			RoiSnapshot:        alarm.ROISnapshot,
			ImageObjectKey:     alarm.ImageObjectKey,
			VideoObjectKey:     "",
			VideoStatus:        alarm.VideoStatus,
		}); err != nil {
			return err
		}

		if alarm.RuleID.Valid {
			channels, chErr := q.ListChannelsByRule(ctx, alarm.RuleID.UUID)
			if chErr != nil {
				return chErr
			}
			for _, ch := range channels {
				if _, dErr := q.CreateAlarmDelivery(ctx, sqlcgen.CreateAlarmDeliveryParams{
					ID:          uuid.Must(uuid.NewV7()),
					AlarmID:     alarm.ID,
					ChannelID:   ch.ID,
					ChannelType: ch.ChannelType,
					Status:      "pending",
					MaxAttempts: 4,
				}); dErr != nil {
					return dErr
				}
				bindings = append(bindings, AlarmChannelBinding{
					ChannelID:   ch.ID,
					ChannelType: ch.ChannelType,
				})
			}
		}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && alarm.EventDedupKey != "" {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, bindings, nil
}

// GetCameraName resolves a camera's display name for realtime WS alert formatting.
func (r *PostgresRepo) GetCameraName(ctx context.Context, cameraID uuid.UUID) string {
	cam, err := r.store.Queries().GetCameraByID(ctx, cameraID)
	if err != nil {
		return cameraID.String()
	}
	return cam.Name
}

// ReloadRules loads all enabled cameras, ROIs, and rules from PostgreSQL into the in-memory rules.Engine.
func (r *PostgresRepo) ReloadRules(ctx context.Context, eng *rules.Engine) error {
	q := r.store.Queries()
	cams, err := q.ListCameras(ctx)
	if err != nil {
		return fmt.Errorf("list cameras for rule reload: %w", err)
	}
	dbRules, err := q.ListEnabledRules(ctx)
	if err != nil {
		return fmt.Errorf("list enabled rules for reload: %w", err)
	}

	roiByID := make(map[uuid.UUID]rules.ROI)
	for _, cam := range cams {
		rois, roiErr := q.ListCameraROIsByCamera(ctx, cam.ID)
		if roiErr != nil {
			return fmt.Errorf("list rois for camera %s: %w", cam.ID, roiErr)
		}
		for _, roi := range rois {
			var pts []rules.Point
			_ = json.Unmarshal(roi.Points, &pts)
			roiByID[roi.ID] = rules.ROI{
				ID:      roi.ID.String(),
				Name:    roi.Name,
				ROIType: roi.RoiType,
				Points:  pts,
			}
		}
	}

	grouped := make(map[string][]rules.Rule)
	for _, cam := range cams {
		grouped[cam.ID.String()] = nil
	}

	for _, dr := range dbRules {
		roi, ok := roiByID[dr.RoiID]
		if !ok {
			continue
		}
		var params rules.RuleParams
		_ = json.Unmarshal(dr.Params, &params)
		grouped[dr.CameraID.String()] = append(grouped[dr.CameraID.String()], rules.Rule{
			ID:                 dr.ID.String(),
			CameraID:           dr.CameraID.String(),
			ROI:                roi,
			AlgorithmVersionID: dr.AlgorithmVersionID.String(),
			Name:               dr.Name,
			RuleMode:           dr.RuleMode,
			Params:             params,
			RecordVideo:        dr.RecordVideo,
			VideoPreSeconds:    int(dr.VideoPreSeconds),
			VideoPostSeconds:   int(dr.VideoPostSeconds),
			Enabled:            dr.Enabled,
		})
	}

	for camID, list := range grouped {
		eng.SetCameraRules(camID, list)
	}
	return nil
}
