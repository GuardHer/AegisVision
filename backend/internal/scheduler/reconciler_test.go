package scheduler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/mediamtx"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/scheduler"
)

type memStateStore struct {
	mu       sync.Mutex
	cameras  []scheduler.CameraInfo
	versions []scheduler.VersionInfo
	rois     []scheduler.ROIInfo
	rules    []scheduler.RuleInfo
	nodes    map[string]scheduler.NodeInfo
	tasks    map[uuid.UUID]scheduler.ExistingTaskInfo
}

func (m *memStateStore) LoadSnapshot(_ context.Context) (scheduler.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	nodes := make([]scheduler.NodeInfo, 0, len(m.nodes))
	for _, n := range m.nodes {
		nodes = append(nodes, n)
	}
	tasks := make([]scheduler.ExistingTaskInfo, 0, len(m.tasks))
	for _, tk := range m.tasks {
		tasks = append(tasks, tk)
	}
	return scheduler.Snapshot{
		Cameras:  append([]scheduler.CameraInfo(nil), m.cameras...),
		Versions: append([]scheduler.VersionInfo(nil), m.versions...),
		ROIs:     append([]scheduler.ROIInfo(nil), m.rois...),
		Rules:    append([]scheduler.RuleInfo(nil), m.rules...),
		Nodes:    nodes,
		Tasks:    tasks,
	}, nil
}

func (m *memStateStore) MarkNodeOffline(_ context.Context, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[nodeID]; ok {
		n.Status = domain.NodeStatusOffline
		m.nodes[nodeID] = n
	}
	return nil
}

func (m *memStateStore) SaveTaskAssignment(_ context.Context, task scheduler.ExistingTaskInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[task.ID] = task
	return nil
}

func (m *memStateStore) StopTask(_ context.Context, taskID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tk, ok := m.tasks[taskID]; ok {
		tk.DesiredState = domain.TaskDesiredStopped
		tk.ActualState = domain.TaskActualStopped
		m.tasks[taskID] = tk
	}
	return nil
}

type recordedCmd struct {
	IngestID string
	Envelope scheduler.AgentCommandEnvelope
}

type spyCommandBus struct {
	mu   sync.Mutex
	cmds []recordedCmd
}

func (s *spyCommandBus) PublishAgentCommand(_ context.Context, ingestInstanceID string, body []byte) error {
	var env scheduler.AgentCommandEnvelope
	_ = json.Unmarshal(body, &env)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds = append(s.cmds, recordedCmd{IngestID: ingestInstanceID, Envelope: env})
	return nil
}

type spyMediaMTX struct {
	mu      sync.Mutex
	upserts []mediamtx.PathSpec
	deletes []string
}

func (s *spyMediaMTX) UpsertPath(_ context.Context, spec mediamtx.PathSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, spec)
	return nil
}

func (s *spyMediaMTX) DeletePath(_ context.Context, streamPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, streamPath)
	return nil
}

func (s *spyMediaMTX) BuildAuthorizedRTSPURL(streamPath, token string) string {
	return "rtsp://mediamtx:8554/" + streamPath + "?token=" + token
}

func TestReconcilerFailoverOnNodeTimeout(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New(): %v", err)
	}
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	_ = rdb.SetAgentRoute(ctx, "node-dead", "ingest-1", time.Minute)
	_ = rdb.SetAgentRoute(ctx, "node-healthy", "ingest-2", time.Minute)

	now := time.Now().UTC()
	camID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())
	oldTaskID := uuid.Must(uuid.NewV7())

	state := &memStateStore{
		cameras: []scheduler.CameraInfo{
			{ID: camID, StreamPath: "cam_failover", SourceURL: "rtsp://cam/1", Enabled: true},
		},
		versions: []scheduler.VersionInfo{
			{ID: verID, RuntimeKey: "py-yolo-v2", DefaultParams: []byte(`{"target_fps":5}`), Status: "active"},
		},
		rois: []scheduler.ROIInfo{
			{ID: roiID, CameraID: camID, Name: "Zone A", ROIType: domain.ROITypeFullFrame, Enabled: true},
		},
		rules: []scheduler.RuleInfo{
			{ID: uuid.Must(uuid.NewV7()), CameraID: camID, ROIID: roiID, AlgorithmVersionID: verID, ScheduleMode: "auto", RecordVideo: true, Enabled: true},
		},
		nodes: map[string]scheduler.NodeInfo{
			"node-dead": {
				NodeID:          "node-dead",
				RuntimeKeys:     []string{"py-yolo-v2"},
				MaxTasks:        8,
				RunningTasks:    1,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now.Add(-20 * time.Second), // timed out (> 15s)
			},
			"node-healthy": {
				NodeID:          "node-healthy",
				RuntimeKeys:     []string{"py-yolo-v2"},
				MaxTasks:        8,
				RunningTasks:    0,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now.Add(-2 * time.Second),
			},
		},
		tasks: map[uuid.UUID]scheduler.ExistingTaskInfo{
			oldTaskID: {
				ID:                 oldTaskID,
				CameraID:           camID,
				AlgorithmVersionID: verID,
				RuntimeKey:         "py-yolo-v2",
				AssignedNodeID:     "node-dead",
				IngestInstanceID:   "ingest-1",
				DesiredState:       domain.TaskDesiredRunning,
				ActualState:        domain.TaskActualRunning,
				ConfigVersion:      1,
			},
		},
	}

	bus := &spyCommandBus{}
	mtx := &spyMediaMTX{}

	rec := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:  "sched-1",
		State:       state,
		Redis:       rdb,
		Bus:         bus,
		MediaMTX:    mtx,
		Logger:      slog.Default(),
		LeaseTTL:    30 * time.Second,
		NodeTimeout: 15 * time.Second,
		NowFunc:     func() time.Time { return now },
	})

	if err := rec.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce() failed: %v", err)
	}

	// 1. Timed-out node must be marked offline
	if state.nodes["node-dead"].Status != domain.NodeStatusOffline {
		t.Errorf("node-dead status = %q, want offline", state.nodes["node-dead"].Status)
	}

	// 2. Task must be migrated to node-healthy
	tk := state.tasks[oldTaskID]
	if tk.AssignedNodeID != "node-healthy" || tk.IngestInstanceID != "ingest-2" {
		t.Fatalf("expected task migrated to (node-healthy, ingest-2), got (%q, %q)", tk.AssignedNodeID, tk.IngestInstanceID)
	}

	// 3. Bus must have emitted revoke to old ingest-1 and assign to new ingest-2
	bus.mu.Lock()
	defer bus.mu.Unlock()
	var sawRevoke, sawAssign bool
	for _, c := range bus.cmds {
		if c.IngestID == "ingest-1" && c.Envelope.CommandType == scheduler.CmdRevokeTask && c.Envelope.NodeID == "node-dead" {
			sawRevoke = true
		}
		if c.IngestID == "ingest-2" && c.Envelope.CommandType == scheduler.CmdAssignTask && c.Envelope.NodeID == "node-healthy" {
			sawAssign = true
			if c.Envelope.AssignTask == nil || c.Envelope.AssignTask.StreamProtocol != "rtsp" || c.Envelope.AssignTask.StreamURL == "" {
				t.Errorf("expected authorized RTSP stream URL in AssignTask command, got %+v", c.Envelope.AssignTask)
			}
		}
	}
	if !sawRevoke || !sawAssign {
		t.Fatalf("expected both revoke (old node) and assign (new node) commands; got %+v", bus.cmds)
	}

	// 4. MediaMTX path synced with RecordVideo=true
	if len(mtx.upserts) != 1 || !mtx.upserts[0].RecordVideo {
		t.Fatalf("expected MediaMTX UpsertPath with RecordVideo=true, got %+v", mtx.upserts)
	}
}

func TestSchedulerLeaderElectionStandbyTakeover(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New(): %v", err)
	}
	defer func() { _ = rdb.Close() }()

	state := &memStateStore{
		nodes: map[string]scheduler.NodeInfo{},
		tasks: map[uuid.UUID]scheduler.ExistingTaskInfo{},
	}

	recA := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:        "sched-A",
		State:             state,
		Redis:             rdb,
		Bus:               &spyCommandBus{},
		MediaMTX:          &spyMediaMTX{},
		Logger:            slog.Default(),
		LeaderTTL:         1 * time.Second,
		LeaderRenewEvery:  100 * time.Millisecond,
		ReconcileInterval: 100 * time.Millisecond,
		LeaseTTL:          30 * time.Second,
		NodeTimeout:       15 * time.Second,
	})
	recB := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:        "sched-B",
		State:             state,
		Redis:             rdb,
		Bus:               &spyCommandBus{},
		MediaMTX:          &spyMediaMTX{},
		Logger:            slog.Default(),
		LeaderTTL:         1 * time.Second,
		LeaderRenewEvery:  100 * time.Millisecond,
		ReconcileInterval: 100 * time.Millisecond,
		LeaseTTL:          30 * time.Second,
		NodeTimeout:       15 * time.Second,
	})

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	doneA := make(chan error, 1)
	go func() { doneA <- recA.Run(ctxA) }()

	// Wait until recA becomes leader
	waitUntil(t, 2*time.Second, recA.IsLeader, "sched-A should acquire leadership")

	// Start standby recB: must NOT become leader while recA holds lock
	doneB := make(chan error, 1)
	go func() { doneB <- recB.Run(ctxB) }()
	time.Sleep(250 * time.Millisecond)
	if recB.IsLeader() {
		t.Fatal("sched-B should stay standby while sched-A holds leadership")
	}

	// Cancel recA (simulating graceful shutdown / crash release) -> recB must take over quickly
	cancelA()
	<-doneA

	waitUntil(t, 2*time.Second, recB.IsLeader, "sched-B should take over leadership after sched-A stops")
	cancelB()
	<-doneB
}

func TestReconcilerSubStreamSyncUpdateTaskAndPendingRetry(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := platformredis.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis.New(): %v", err)
	}
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	now := time.Now().UTC()
	camID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())

	state := &memStateStore{
		cameras: []scheduler.CameraInfo{
			{
				ID:           camID,
				StreamPath:   "cam_sub_sync",
				SourceURL:    "rtsp://cam/main",
				SubStreamURL: "rtsp://cam/sub",
				Enabled:      true,
			},
		},
		versions: []scheduler.VersionInfo{
			{ID: verID, RuntimeKey: "py-yolo-v2", DefaultParams: []byte(`{"target_fps":5}`), Status: "active"},
		},
		rois: []scheduler.ROIInfo{
			{ID: roiID, CameraID: camID, Name: "Zone 1", ROIType: domain.ROITypeFullFrame, Enabled: true},
		},
		rules: []scheduler.RuleInfo{
			{ID: uuid.Must(uuid.NewV7()), CameraID: camID, ROIID: roiID, AlgorithmVersionID: verID, ScheduleMode: "auto", Enabled: true},
		},
		nodes: map[string]scheduler.NodeInfo{
			"node-1": {
				NodeID:          "node-1",
				RuntimeKeys:     []string{"py-yolo-v2"},
				MaxTasks:        8,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now,
			},
		},
		tasks: map[uuid.UUID]scheduler.ExistingTaskInfo{},
	}
	bus := &spyCommandBus{}
	mtx := &spyMediaMTX{}
	rec := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:  "sched-sub",
		State:       state,
		Redis:       rdb,
		Bus:         bus,
		MediaMTX:    mtx,
		Logger:      slog.Default(),
		LeaseTTL:    30 * time.Second,
		NodeTimeout: 15 * time.Second,
		NowFunc:     func() time.Time { return now },
	})

	// Pass 1: route not yet registered -> task saved as pending, no AssignTask emitted yet; sub-stream synced.
	if err := rec.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce pass 1: %v", err)
	}
	if len(mtx.upserts) != 2 || mtx.upserts[1].StreamPath != "cam_sub_sync_sub" {
		t.Fatalf("expected main + _sub MediaMTX upserts, got %+v", mtx.upserts)
	}
	if len(bus.cmds) != 0 {
		t.Fatalf("expected 0 commands when route missing, got %d", len(bus.cmds))
	}

	// Pass 2: agent route arrives -> pending task MUST retry CmdAssignTask!
	_ = rdb.SetAgentRoute(ctx, "node-1", "ingest-1", time.Minute)
	if err := rec.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce pass 2: %v", err)
	}
	if len(bus.cmds) != 1 || bus.cmds[0].Envelope.CommandType != scheduler.CmdAssignTask {
		t.Fatalf("expected pending task to retry CmdAssignTask, got %+v", bus.cmds)
	}

	// Mark task running as agent ACK would do, then mutate ROI -> Pass 3 MUST emit CmdUpdateTask!
	for id, tk := range state.tasks {
		tk.ActualState = domain.TaskActualRunning
		state.tasks[id] = tk
	}
	state.rois[0].Name = "Zone 1 Updated"
	if err := rec.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce pass 3: %v", err)
	}
	if len(bus.cmds) != 2 || bus.cmds[1].Envelope.CommandType != scheduler.CmdUpdateTask {
		t.Fatalf("expected CmdUpdateTask after ROI change on same node, got %+v", bus.cmds)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
