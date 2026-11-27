package evidence

import (
	"context"

	"github.com/google/uuid"

	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// PostgresVideoRepo adapts postgres.Store to evidence.VideoRepo.
type PostgresVideoRepo struct {
	store *postgres.Store
}

// NewPostgresVideoRepo constructs a PostgreSQL-backed VideoRepo.
func NewPostgresVideoRepo(store *postgres.Store) *PostgresVideoRepo {
	return &PostgresVideoRepo{store: store}
}

// GetCameraStreamPath returns a camera's MediaMTX stream_path.
func (p *PostgresVideoRepo) GetCameraStreamPath(ctx context.Context, cameraID uuid.UUID) (string, error) {
	cam, err := p.store.Queries().GetCameraByID(ctx, cameraID)
	if err != nil {
		return "", err
	}
	return cam.StreamPath, nil
}

// UpdateAlarmVideoEvidence backfills video_object_key and video_status on alarms.
func (p *PostgresVideoRepo) UpdateAlarmVideoEvidence(ctx context.Context, alarmID uuid.UUID, objectKey, status string) error {
	return p.store.Queries().UpdateAlarmVideoEvidence(ctx, sqlcgen.UpdateAlarmVideoEvidenceParams{
		ID:             alarmID,
		VideoObjectKey: objectKey,
		VideoStatus:    status,
	})
}
