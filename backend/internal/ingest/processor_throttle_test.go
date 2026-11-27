package ingest_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/ingest"
	"aegisvision/backend/internal/rules"
)

type countingLastFrameRepo struct {
	updates atomic.Int32
}

func (r *countingLastFrameRepo) UpsertAgentNode(context.Context, ingest.NodeRegistration) error {
	return nil
}

func (r *countingLastFrameRepo) UpdateAgentHeartbeat(context.Context, string, int, []byte) error {
	return nil
}

func (r *countingLastFrameRepo) UpdateTaskState(context.Context, string, string, string) error {
	return nil
}

func (r *countingLastFrameRepo) UpdateTaskLastFrame(context.Context, string, time.Time) error {
	r.updates.Add(1)
	return nil
}

func (r *countingLastFrameRepo) CreateAlarmIdempotent(context.Context, ingest.PersistedAlarm) (bool, []ingest.AlarmChannelBinding, error) {
	return false, nil, nil
}

func (r *countingLastFrameRepo) GetCameraName(context.Context, uuid.UUID) string {
	return "cam"
}

func TestProcessorThrottlesLastFrameUpdates(t *testing.T) {
	repo := &countingLastFrameRepo{}
	proc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine:                  rules.NewEngine(),
		Repo:                    repo,
		LastFrameUpdateInterval: 5 * time.Second,
	})

	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	camID := uuid.Must(uuid.NewV7()).String()
	taskID := uuid.Must(uuid.NewV7()).String()

	// 10 frames within 4.5s -> only the 1st frame should write to Postgres.
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i*500) * time.Millisecond)
		if err := proc.ProcessLiveFrame(context.Background(), &agentv1.DetectionFrame{
			TaskId:      taskID,
			CameraId:    camID,
			TimestampMs: ts.UnixMilli(),
		}); err != nil {
			t.Fatalf("ProcessLiveFrame %d: %v", i, err)
		}
	}
	if got := repo.updates.Load(); got != 1 {
		t.Fatalf("expected 1 throttled UpdateTaskLastFrame within 5s window, got %d", got)
	}

	// Next frame at +6s exceeds 5s throttle -> triggers 2nd write.
	if err := proc.ProcessLiveFrame(context.Background(), &agentv1.DetectionFrame{
		TaskId:      taskID,
		CameraId:    camID,
		TimestampMs: base.Add(6 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("ProcessLiveFrame +6s: %v", err)
	}
	if got := repo.updates.Load(); got != 2 {
		t.Fatalf("expected 2nd UpdateTaskLastFrame after 5s interval, got %d", got)
	}
}
