package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/ingest"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/rules"
)

type spyIngestRepo struct {
	mu               sync.Mutex
	dedupKeys        map[string]uuid.UUID
	alarms           []ingest.PersistedAlarm
	compliantCycles  []ingest.PersistedSOPCycle
	nodes            map[string]string
	lastFrames       map[string]time.Time
	taskStates       map[string]string
}

func newSpyRepo() *spyIngestRepo {
	return &spyIngestRepo{
		dedupKeys:  make(map[string]uuid.UUID),
		nodes:      make(map[string]string),
		lastFrames: make(map[string]time.Time),
		taskStates: make(map[string]string),
	}
}

func (r *spyIngestRepo) UpsertAgentNode(_ context.Context, node ingest.NodeRegistration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[node.NodeID] = node.Hostname
	return nil
}

func (r *spyIngestRepo) UpdateAgentHeartbeat(_ context.Context, nodeID string, _ int, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[nodeID] = "heartbeat"
	return nil
}

func (r *spyIngestRepo) UpdateTaskState(_ context.Context, taskID, actualState, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskStates[taskID] = actualState
	return nil
}

func (r *spyIngestRepo) UpdateTaskLastFrame(_ context.Context, taskID string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFrames[taskID] = ts
	return nil
}

func (r *spyIngestRepo) CreateAlarmIdempotent(_ context.Context, alarm ingest.PersistedAlarm) (bool, []ingest.AlarmChannelBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if alarm.EventDedupKey != "" {
		if _, exists := r.dedupKeys[alarm.EventDedupKey]; exists {
			return false, nil, nil // duplicate!
		}
		r.dedupKeys[alarm.EventDedupKey] = alarm.ID
	}
	r.alarms = append(r.alarms, alarm)
	return true, []ingest.AlarmChannelBinding{
		{ChannelID: uuid.Must(uuid.NewV7()), ChannelType: "webhook"},
	}, nil
}

func (r *spyIngestRepo) RecordCompliantSOPCycle(_ context.Context, cycle ingest.PersistedSOPCycle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compliantCycles = append(r.compliantCycles, cycle)
	return nil
}

func (r *spyIngestRepo) GetCameraName(_ context.Context, _ uuid.UUID) string {
	return "Workshop Cam 1"
}

type spyIngestMQ struct {
	mu           sync.Mutex
	alarmMsgs    [][]byte
	evidenceMsgs [][]byte
}

func (m *spyIngestMQ) PublishAlarm(_ context.Context, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alarmMsgs = append(m.alarmMsgs, append([]byte(nil), body...))
	return nil
}

func (m *spyIngestMQ) PublishEvidenceRecord(_ context.Context, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evidenceMsgs = append(m.evidenceMsgs, append([]byte(nil), body...))
	return nil
}

func startTestIngestServer(t *testing.T, gw *ingest.Gateway) (agentv1.AgentGatewayClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	agentv1.RegisterAgentGatewayServer(grpcSrv, gw)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc dial bufconn: %v", err)
	}
	return agentv1.NewAgentGatewayClient(conn), func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	}
}

// Review Focus #2: duplicate ReportBufferedEvents with identical event_id must be idempotent:
// 1 alarm persisted, 1 MQ message published, and BOTH calls receive accepted EventAck.
func TestIngestBufferedEventIdempotency(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New(): %v", err)
	}
	defer func() { _ = rdb.Close() }()

	localObj, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal(): %v", err)
	}

	camID := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	ruleID := uuid.Must(uuid.NewV7())

	eng := rules.NewEngine()
	eng.SetCameraRules(camID.String(), []rules.Rule{
		{
			ID:                 ruleID.String(),
			CameraID:           camID.String(),
			ROI:                rules.ROI{ID: roiID.String(), Name: "Zone 1", ROIType: domain.ROITypeFullFrame},
			AlgorithmVersionID: verID.String(),
			Name:               "No Helmet",
			RuleMode:           domain.RuleModeAnomalyDetect,
			Params: rules.RuleParams{
				Labels:            []string{"no_helmet"},
				MinConfidence:     0.5,
				ConsecutiveFrames: 1,
				CooldownSeconds:   30,
				Severity:          domain.SeverityCritical,
			},
			RecordVideo: true,
			Enabled:     true,
		},
	})

	repo := newSpyRepo()
	mq := &spyIngestMQ{}
	proc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine:  eng,
		Repo:    repo,
		Redis:   rdb,
		MQ:      mq,
		Storage: localObj,
		Logger:  slog.Default(),
	})
	gw := ingest.NewGateway(ingest.GatewayDeps{
		InstanceID: "ingest-test-1",
		AgentToken: "cluster-secret",
		Repo:       repo,
		Redis:      rdb,
		Processor:  proc,
		Logger:     slog.Default(),
	})

	client, cleanup := startTestIngestServer(t, gw)
	defer cleanup()

	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-agent-token", "cluster-secret",
		"x-node-id", "gpu-node-1",
	)), 10*time.Second)
	defer cancel()

	stream, err := client.ReportBufferedEvents(ctx)
	if err != nil {
		t.Fatalf("ReportBufferedEvents() open failed: %v", err)
	}

	dupEvent := &agentv1.BufferedEvent{
		EventId:    "evt-offline-001",
		CachedAtMs: time.Now().UnixMilli(),
		Frame: &agentv1.DetectionFrame{
			TaskId:             uuid.Must(uuid.NewV7()).String(),
			CameraId:           camID.String(),
			AlgorithmVersionId: verID.String(),
			TimestampMs:        time.Now().UnixMilli(),
			Detections: []*agentv1.Detection{
				{Bbox: []float32{0.1, 0.1, 0.4, 0.6}, Label: "no_helmet", Confidence: 0.92, IsAbnormal: true},
			},
			SnapshotJpeg: []byte{0xFF, 0xD8, 0xFF, 0xE0, 'J', 'P', 'G', 0xFF, 0xD9},
		},
	}

	// Send exact same BufferedEvent twice (simulating network drop before first Ack arrived at Agent)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := stream.Send(dupEvent); err != nil {
			t.Fatalf("send buffered event attempt %d: %v", attempt, err)
		}
		ack, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv event ack attempt %d: %v", attempt, err)
		}
		if ack.GetEventId() != "evt-offline-001" || !ack.GetAccepted() {
			t.Fatalf("attempt %d unexpected ack: %+v", attempt, ack)
		}
	}
	_ = stream.CloseSend()

	repo.mu.Lock()
	alarmCount := len(repo.alarms)
	repo.mu.Unlock()
	if alarmCount != 1 {
		t.Fatalf("expected exactly 1 persisted alarm for duplicate event_id, got %d", alarmCount)
	}

	mq.mu.Lock()
	mqAlarms := len(mq.alarmMsgs)
	mqEvidence := len(mq.evidenceMsgs)
	mq.mu.Unlock()
	if mqAlarms != 1 {
		t.Fatalf("expected exactly 1 RabbitMQ alarm publish, got %d", mqAlarms)
	}
	if mqEvidence != 1 {
		t.Fatalf("expected exactly 1 RabbitMQ video evidence publish, got %d", mqEvidence)
	}
}

func TestIngestControlStreamRoutingAndLiveFrameBroadcast(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New(): %v", err)
	}
	defer func() { _ = rdb.Close() }()

	camID := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())

	eng := rules.NewEngine()
	eng.SetCameraRules(camID.String(), []rules.Rule{
		{
			ID:                 uuid.Must(uuid.NewV7()).String(),
			CameraID:           camID.String(),
			ROI:                rules.ROI{ID: roiID.String(), Name: "Zone", ROIType: domain.ROITypeFullFrame},
			AlgorithmVersionID: verID.String(),
			Name:               "Person Count",
			RuleMode:           domain.RuleModeCountThreshold,
			Params:             rules.RuleParams{Labels: []string{"person"}, MinConfidence: 0.5, MaxCount: intPtr(10)},
			Enabled:            true,
		},
	})

	repo := newSpyRepo()
	proc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine:  eng,
		Repo:    repo,
		Redis:   rdb,
		MQ:      &spyIngestMQ{},
		Storage: nil,
		Logger:  slog.Default(),
	})
	gw := ingest.NewGateway(ingest.GatewayDeps{
		InstanceID: "ingest-node-A",
		AgentToken: "secret-tok",
		Repo:       repo,
		Redis:      rdb,
		Processor:  proc,
		Logger:     slog.Default(),
	})

	client, cleanup := startTestIngestServer(t, gw)
	defer cleanup()

	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-agent-token", "secret-tok",
		"x-node-id", "gpu-01",
	)), 10*time.Second)
	defer cancel()

	// 1. ControlStream registration & downstream command forwarding
	ctrl, err := client.ControlStream(ctx)
	if err != nil {
		t.Fatalf("ControlStream(): %v", err)
	}
	if err := ctrl.Send(&agentv1.AgentUpstream{
		Payload: &agentv1.AgentUpstream_Register{
			Register: &agentv1.RegisterRequest{
				NodeId:      "gpu-01",
				Hostname:    "gpu-host-01",
				IpAddr:      "192.168.0.50",
				RuntimeKeys: []string{"py-yolo-v2"},
				MaxTasks:    8,
			},
		},
	}); err != nil {
		t.Fatalf("send RegisterRequest: %v", err)
	}

	// Wait for route registration in Redis
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		route, rErr := rdb.GetAgentRoute(ctx, "gpu-01")
		if rErr == nil && route == "ingest-node-A" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	route, err := rdb.GetAgentRoute(ctx, "gpu-01")
	if err != nil || route != "ingest-node-A" {
		t.Fatalf("Redis agent:route:gpu-01 = (%q, %v), want ingest-node-A", route, err)
	}

	// Route a scheduler command envelope into gateway -> Agent must receive it on ControlStream
	cmdRaw, _ := json.Marshal(map[string]any{
		"command_id":   "cmd-1",
		"node_id":      "gpu-01",
		"command_type": "assign_task",
		"assign_task": map[string]any{
			"task_id":           "task-1",
			"camera_id":         camID.String(),
			"runtime_key":       "py-yolo-v2",
			"stream_url":        "rtsp://mediamtx:8554/cam1?token=abc",
			"stream_protocol":   "rtsp",
			"lease_id":          "lease-1",
			"lease_ttl_seconds": 30,
		},
	})
	if err := gw.HandleRoutedCommand(ctx, cmdRaw); err != nil {
		t.Fatalf("HandleRoutedCommand(): %v", err)
	}

	down, err := ctrl.Recv()
	if err != nil {
		t.Fatalf("ctrl.Recv(): %v", err)
	}
	if down.GetAssignTask().GetTaskId() != "task-1" || down.GetAssignTask().GetStreamProtocol() != "rtsp" {
		t.Fatalf("unexpected downstream AssignTask: %+v", down)
	}

	// 2. StreamLiveFrames publishes transient frame_detections to Redis Pub/Sub
	framesCh, unsub, err := rdb.SubscribeFrames(ctx, camID.String())
	if err != nil {
		t.Fatalf("SubscribeFrames(): %v", err)
	}
	defer func() { _ = unsub() }()

	liveStream, err := client.StreamLiveFrames(ctx)
	if err != nil {
		t.Fatalf("StreamLiveFrames(): %v", err)
	}
	if err := liveStream.Send(&agentv1.LiveFrameBatch{
		NodeId: "gpu-01",
		Frames: []*agentv1.DetectionFrame{
			{
				TaskId:             "task-1",
				CameraId:           camID.String(),
				AlgorithmVersionId: verID.String(),
				TimestampMs:        time.Now().UnixMilli(),
				Detections: []*agentv1.Detection{
					{Bbox: []float32{0.1, 0.2, 0.3, 0.7}, Label: "person", Confidence: 0.91},
				},
			},
		},
	}); err != nil {
		t.Fatalf("liveStream.Send(): %v", err)
	}
	ack, err := liveStream.CloseAndRecv()
	if err != nil || ack.GetReceivedFrames() != 1 {
		t.Fatalf("liveStream.CloseAndRecv() = (%+v, %v), want received_frames=1", ack, err)
	}

	select {
	case msg := <-framesCh:
		var parsed struct {
			Type      string         `json:"type"`
			CameraID  string         `json:"camera_id"`
			ROICounts map[string]int `json:"roi_counts"`
		}
		if err := json.Unmarshal(msg.Payload, &parsed); err != nil {
			t.Fatalf("unmarshal published frame: %v", err)
		}
		if parsed.Type != "frame_detections" || parsed.CameraID != camID.String() || parsed.ROICounts[roiID.String()] != 1 {
			t.Fatalf("unexpected published frame_detections: %+v", parsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Redis Pub/Sub live frame broadcast")
	}

	_ = errors.New("")
}

func intPtr(v int) *int { return &v }
