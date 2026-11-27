package notifier

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// PostgresDeliveryStore adapts postgres.Store to notifier.DeliveryStore.
type PostgresDeliveryStore struct {
	store *postgres.Store
}

// NewPostgresDeliveryStore constructs a PostgreSQL-backed DeliveryStore.
func NewPostgresDeliveryStore(store *postgres.Store) *PostgresDeliveryStore {
	return &PostgresDeliveryStore{store: store}
}

// GetAlarmContext loads alarm details enriched with camera, ROI, and algorithm metadata.
func (p *PostgresDeliveryStore) GetAlarmContext(ctx context.Context, alarmID uuid.UUID) (AlarmContext, error) {
	q := p.store.Queries()
	a, err := q.GetAlarmByID(ctx, alarmID)
	if err != nil {
		return AlarmContext{}, fmt.Errorf("get alarm %s: %w", alarmID, err)
	}

	out := AlarmContext{
		AlarmID:        a.ID,
		TriggeredAt:    a.TriggeredAt,
		Severity:       a.Severity,
		RuleMode:       a.RuleMode,
		Title:          a.Title,
		Camera:         CameraRef{ID: a.CameraID.String()},
		DetectedCount:  int(a.DetectedCount),
		Detections:     a.DetectionsSnapshot,
		ImageObjectKey: a.ImageObjectKey,
		VideoObjectKey: a.VideoObjectKey,
		VideoStatus:    a.VideoStatus,
	}

	if cam, camErr := q.GetCameraByID(ctx, a.CameraID); camErr == nil {
		out.Camera.Name = cam.Name
		out.Camera.Location = cam.Location
	}
	if a.RoiID.Valid {
		out.ROI.ID = a.RoiID.UUID.String()
		if roi, roiErr := q.GetCameraROIByID(ctx, a.RoiID.UUID); roiErr == nil {
			out.ROI.Name = roi.Name
		}
	} else if len(a.RoiSnapshot) > 0 {
		var snap struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(a.RoiSnapshot, &snap) == nil {
			out.ROI = ROIRef{ID: snap.ID, Name: snap.Name}
		}
	}
	if a.AlgorithmVersionID.Valid {
		if ver, verErr := q.GetAlgorithmVersionByID(ctx, a.AlgorithmVersionID.UUID); verErr == nil {
			out.Algorithm.Version = ver.Version
			if algo, algoErr := q.GetAlgorithmByID(ctx, ver.AlgorithmID); algoErr == nil {
				out.Algorithm.Key = algo.AlgorithmKey
			}
		}
	}

	return out, nil
}

// ListDeliveriesForAlarm loads all alarm_deliveries for alarmID joined with alarm_channels config.
func (p *PostgresDeliveryStore) ListDeliveriesForAlarm(ctx context.Context, alarmID uuid.UUID) ([]DeliveryRecord, error) {
	q := p.store.Queries()
	rows, err := q.ListDeliveriesByAlarm(ctx, alarmID)
	if err != nil {
		return nil, err
	}
	out := make([]DeliveryRecord, 0, len(rows))
	for _, row := range rows {
		ch, chErr := q.GetAlarmChannelByID(ctx, row.ChannelID)
		if chErr != nil || !ch.Enabled {
			continue
		}
		out = append(out, toDeliveryRecord(row, ch.Config))
	}
	return out, nil
}

// GetDelivery loads a single delivery record and its channel configuration.
func (p *PostgresDeliveryStore) GetDelivery(ctx context.Context, deliveryID uuid.UUID) (DeliveryRecord, error) {
	q := p.store.Queries()
	row, err := q.GetAlarmDeliveryByID(ctx, deliveryID)
	if err != nil {
		return DeliveryRecord{}, err
	}
	ch, err := q.GetAlarmChannelByID(ctx, row.ChannelID)
	if err != nil {
		return DeliveryRecord{}, err
	}
	return toDeliveryRecord(row, ch.Config), nil
}

// UpdateDeliveryResult persists attempt outcome into alarm_deliveries.
func (p *PostgresDeliveryStore) UpdateDeliveryResult(ctx context.Context, u DeliveryUpdate) error {
	_, err := p.store.Queries().UpdateAlarmDeliveryResult(ctx, sqlcgen.UpdateAlarmDeliveryResultParams{
		ID:             u.DeliveryID,
		Status:         u.Status,
		Attempts:       int32(u.Attempts),       // #nosec G115 -- attempt count
		LastStatusCode: int32(u.LastStatusCode), // #nosec G115 -- HTTP status code
		LastLatencyMs:  int32(u.LastLatencyMs),  // #nosec G115 -- latency ms
		LastError:      u.LastError,
	})
	return err
}

func toDeliveryRecord(row sqlcgen.AlarmDelivery, channelConfig []byte) DeliveryRecord {
	return DeliveryRecord{
		ID:             row.ID,
		AlarmID:        row.AlarmID,
		ChannelID:      row.ChannelID,
		ChannelType:    row.ChannelType,
		ChannelConfig:  channelConfig,
		Status:         row.Status,
		Attempts:       int(row.Attempts),
		MaxAttempts:    int(row.MaxAttempts),
		LastStatusCode: int(row.LastStatusCode),
		LastLatencyMs:  int(row.LastLatencyMs),
		LastError:      row.LastError,
	}
}
