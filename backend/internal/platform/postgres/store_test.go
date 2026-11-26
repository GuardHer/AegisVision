package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func testPostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("AEGIS_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("AEGIS_TEST_POSTGRES_DSN / AEGIS_POSTGRES_DSN not set; skipping postgres test")
	}
	return dsn
}

func TestActiveInferenceTaskUniqueness(t *testing.T) {
	dsn := testPostgresDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations() failed: %v", err)
	}

	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New() failed: %v", err)
	}
	defer store.Close()

	algoID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	camID := uuid.Must(uuid.NewV7())
	task1ID := uuid.Must(uuid.NewV7())
	task2ID := uuid.Must(uuid.NewV7())
	task3ID := uuid.Must(uuid.NewV7())

	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_, _ = store.Pool().Exec(cleanCtx, "DELETE FROM inference_tasks WHERE id IN ($1, $2, $3)", task1ID, task2ID, task3ID)
		_, _ = store.Pool().Exec(cleanCtx, "DELETE FROM cameras WHERE id = $1", camID)
		_, _ = store.Pool().Exec(cleanCtx, "DELETE FROM algorithm_versions WHERE id = $1", verID)
		_, _ = store.Pool().Exec(cleanCtx, "DELETE FROM algorithms WHERE id = $1", algoID)
	})

	q := store.Queries()

	if _, err := q.CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
		ID:              algoID,
		AlgorithmKey:    "test_algo_" + algoID.String()[24:],
		Name:            "Test Person Detect",
		Description:     "test algorithm",
		SupportedLabels: []byte(`["person"]`),
	}); err != nil {
		t.Fatalf("CreateAlgorithm() failed: %v", err)
	}

	if _, err := q.CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{
		ID:            verID,
		AlgorithmID:   algoID,
		Version:       "v1.0.0",
		RuntimeKey:    "py-yolo-v1",
		LaunchSpec:    []byte(`{}`),
		DefaultParams: []byte(`{"target_fps":5}`),
		Status:        "active",
	}); err != nil {
		t.Fatalf("CreateAlgorithmVersion() failed: %v", err)
	}

	if _, err := q.CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID:           camID,
		Name:         "Test Camera",
		Location:     "Workshop A",
		RtspUrl:      "rtsp://127.0.0.1:8554/test",
		SubStreamUrl: "",
		StreamPath:   "cam_" + camID.String()[24:],
		CodecHint:    "h264",
		Status:       "offline",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("CreateCamera() failed: %v", err)
	}

	// 1. Insert first running task: MUST succeed.
	if _, err := q.CreateInferenceTask(ctx, sqlcgen.CreateInferenceTaskParams{
		ID:                 task1ID,
		CameraID:           camID,
		AlgorithmVersionID: verID,
		RuntimeKey:         "py-yolo-v1",
		DesiredState:       "running",
		ActualState:        "pending",
		ConfigVersion:      1,
	}); err != nil {
		t.Fatalf("CreateInferenceTask(running #1) failed: %v", err)
	}

	// 2. Insert duplicate running task for same (camera_id, algorithm_version_id): MUST fail with unique violation (23505).
	_, err = q.CreateInferenceTask(ctx, sqlcgen.CreateInferenceTaskParams{
		ID:                 task2ID,
		CameraID:           camID,
		AlgorithmVersionID: verID,
		RuntimeKey:         "py-yolo-v1",
		DesiredState:       "running",
		ActualState:        "pending",
		ConfigVersion:      1,
	})
	if err == nil {
		t.Fatal("expected unique violation for duplicate running inference_task, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected PostgreSQL unique violation (23505), got: %v", err)
	}

	// 3. Insert stopped task for same (camera_id, algorithm_version_id): MUST succeed.
	if _, err := q.CreateInferenceTask(ctx, sqlcgen.CreateInferenceTaskParams{
		ID:                 task3ID,
		CameraID:           camID,
		AlgorithmVersionID: verID,
		RuntimeKey:         "py-yolo-v1",
		DesiredState:       "stopped",
		ActualState:        "stopped",
		ConfigVersion:      1,
	}); err != nil {
		t.Fatalf("CreateInferenceTask(stopped) should succeed alongside running task, got: %v", err)
	}
}

func TestStoreWithTxRollbackAndCommit(t *testing.T) {
	dsn := testPostgresDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations() failed: %v", err)
	}

	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New() failed: %v", err)
	}
	defer store.Close()

	rolledBackID := uuid.Must(uuid.NewV7())
	sentinelErr := errors.New("force rollback")
	err = store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
			ID:              rolledBackID,
			AlgorithmKey:    "tx_rb_" + rolledBackID.String()[24:],
			Name:            "Rollback Algo",
			Description:     "",
			SupportedLabels: []byte(`[]`),
		}); err != nil {
			return err
		}
		return sentinelErr
	})
	if !errors.Is(err, sentinelErr) {
		t.Fatalf("WithTx() err = %v, want sentinelErr", err)
	}

	if _, err := store.Queries().GetAlgorithmByID(ctx, rolledBackID); err == nil {
		t.Fatal("expected rolled-back algorithm not to exist, but GetAlgorithmByID succeeded")
	}
}
