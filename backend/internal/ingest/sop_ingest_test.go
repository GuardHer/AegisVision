package ingest_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/ingest"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/rules"
)

func TestProcessorSOPProgressBroadcastCyclesAndSnapshotFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	camID := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	ruleID := uuid.Must(uuid.NewV7())
	reqTrue := true

	eng := rules.NewEngine()
	eng.SetCameraRules(camID.String(), []rules.Rule{
		{
			ID:                 ruleID.String(),
			CameraID:           camID.String(),
			ROI:                rules.ROI{ID: roiID.String(), Name: "Bench-1", ROIType: domain.ROITypeFullFrame},
			AlgorithmVersionID: verID.String(),
			Name:               "Bench-SOP",
			RuleMode:           domain.RuleModeSOPSequence,
			Params: rules.RuleParams{
				TriggerMode:             "first_step",
				HeartbeatTimeoutSeconds: 5,
				ViolationTypes:          []string{"missing_step", "out_of_order", "step_timeout"},
				Steps: []rules.SOPStepSpec{
					{StepID: "s1", Name: "Step1", Labels: []string{"s1"}, MinConfidence: 0.5, Required: &reqTrue},
					{StepID: "s2", Name: "Step2", Labels: []string{"s2"}, MinConfidence: 0.5, Required: &reqTrue},
				},
			},
			Enabled: true,
		},
	})

	repo := newSpyRepo()
	mq := &spyIngestMQ{}
	proc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine: eng,
		Repo:   repo,
		Redis:  rdb,
		MQ:     mq,
		Logger: slog.Default(),
	})

	ctx := context.Background()
	framesCh, unsub, err := rdb.SubscribeFrames(ctx, camID.String())
	if err != nil {
		t.Fatalf("SubscribeFrames: %v", err)
	}
	defer func() { _ = unsub() }()

	t0 := time.Now().UTC()
	// 1. Frame s1 -> broadcasts sop_progress with state=in_progress
	if err := proc.ProcessLiveFrame(ctx, &agentv1.DetectionFrame{
		CameraId:           camID.String(),
		AlgorithmVersionId: verID.String(),
		TimestampMs:        t0.UnixMilli(),
		Detections:         []*agentv1.Detection{{Bbox: []float32{0.2, 0.2, 0.4, 0.4}, Label: "s1", Confidence: 0.9}},
	}); err != nil {
		t.Fatalf("ProcessLiveFrame s1: %v", err)
	}
	select {
	case msg := <-framesCh:
		var parsed struct {
			SOPProgress []rules.SOPRuleProgress `json:"sop_progress"`
		}
		if err := json.Unmarshal(msg.Payload, &parsed); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if len(parsed.SOPProgress) != 1 || parsed.SOPProgress[0].State != "in_progress" {
			t.Fatalf("expected sop_progress state=in_progress, got %+v", parsed.SOPProgress)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sop_progress broadcast")
	}

	// 2. Frame s2 -> records compliant SOP cycle
	if err := proc.ProcessLiveFrame(ctx, &agentv1.DetectionFrame{
		CameraId:           camID.String(),
		AlgorithmVersionId: verID.String(),
		TimestampMs:        t0.Add(time.Second).UnixMilli(),
		Detections:         []*agentv1.Detection{{Bbox: []float32{0.2, 0.2, 0.4, 0.4}, Label: "s2", Confidence: 0.9}},
	}); err != nil {
		t.Fatalf("ProcessLiveFrame s2: %v", err)
	}
	if len(repo.compliantCycles) != 1 {
		t.Fatalf("expected 1 compliant SOP cycle persisted, got %d", len(repo.compliantCycles))
	}

	// 3. Out-of-order s2 from idle (no snapshot_jpeg) -> persists alarm with sop_trace AND enqueues async snapshot fallback!
	if err := proc.ProcessLiveFrame(ctx, &agentv1.DetectionFrame{
		CameraId:           camID.String(),
		AlgorithmVersionId: verID.String(),
		TimestampMs:        t0.Add(2 * time.Second).UnixMilli(),
		Detections:         []*agentv1.Detection{{Bbox: []float32{0.2, 0.2, 0.4, 0.4}, Label: "s2", Confidence: 0.9}},
	}); err != nil {
		t.Fatalf("ProcessLiveFrame violation: %v", err)
	}
	if len(repo.alarms) != 1 || len(repo.alarms[0].SOPTrace) == 0 {
		t.Fatalf("expected 1 violated SOP alarm with sop_trace, got %+v", repo.alarms)
	}
	if len(mq.evidenceMsgs) != 1 {
		t.Fatalf("expected 1 async snapshot fallback job in evidence.record, got %d", len(mq.evidenceMsgs))
	}
	var job struct {
		Mode    string `json:"mode"`
		AlarmID string `json:"alarm_id"`
	}
	_ = json.Unmarshal(mq.evidenceMsgs[0], &job)
	if job.Mode != "snapshot" || job.AlarmID != repo.alarms[0].ID.String() {
		t.Fatalf("expected mode=snapshot for alarm %s, got %+v", repo.alarms[0].ID, job)
	}
}
