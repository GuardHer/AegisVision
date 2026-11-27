package agent_test

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	runtimev1 "aegisvision/backend/gen/go/runtime/v1"
	"aegisvision/backend/internal/agent"
)

type fakeRuntimeServer struct {
	runtimev1.UnimplementedAlgorithmRuntimeServer
	runtimeKey string

	mu     sync.Mutex
	tasks  map[string]*runtimev1.RuntimeAssignTaskRequest
	renews int
}

func newFakeRuntimeServer(runtimeKey string) *fakeRuntimeServer {
	return &fakeRuntimeServer{
		runtimeKey: runtimeKey,
		tasks:      make(map[string]*runtimev1.RuntimeAssignTaskRequest),
	}
}

func (f *fakeRuntimeServer) Health(_ context.Context, _ *runtimev1.HealthRequest) (*runtimev1.HealthResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &runtimev1.HealthResponse{
		Ready:       true,
		RuntimeKey:  f.runtimeKey,
		Version:     "test-1.0",
		ActiveTasks: int32(len(f.tasks)),
	}, nil
}

func (f *fakeRuntimeServer) AssignTask(_ context.Context, req *runtimev1.RuntimeAssignTaskRequest) (*runtimev1.RuntimeTaskReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[req.GetTaskId()] = req
	return &runtimev1.RuntimeTaskReply{Accepted: true}, nil
}

func (f *fakeRuntimeServer) UpdateTask(_ context.Context, _ *runtimev1.RuntimeUpdateTaskRequest) (*runtimev1.RuntimeTaskReply, error) {
	return &runtimev1.RuntimeTaskReply{Accepted: true}, nil
}

func (f *fakeRuntimeServer) RevokeTask(_ context.Context, req *runtimev1.RuntimeRevokeTaskRequest) (*runtimev1.RuntimeTaskReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tasks, req.GetTaskId())
	return &runtimev1.RuntimeTaskReply{Accepted: true}, nil
}

func (f *fakeRuntimeServer) RenewLease(_ context.Context, _ *runtimev1.RuntimeRenewLeaseRequest) (*runtimev1.RuntimeTaskReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	return &runtimev1.RuntimeTaskReply{Accepted: true}, nil
}

func (f *fakeRuntimeServer) StreamResults(_ *runtimev1.StreamResultsRequest, stream grpc.ServerStreamingServer[runtimev1.RuntimeResult]) error {
	<-stream.Context().Done()
	return nil
}

type fakeManagedProcess struct {
	lis     *bufconn.Listener
	grpcSrv *grpc.Server
	stopped atomic.Bool
	doneCh  chan struct{}
}

func (p *fakeManagedProcess) Address() string { return "passthrough:///fake-rt" }

func (p *fakeManagedProcess) Stop() error {
	if p.stopped.CompareAndSwap(false, true) {
		p.grpcSrv.Stop()
		_ = p.lis.Close()
		close(p.doneCh)
	}
	return nil
}

func (p *fakeManagedProcess) Wait() error {
	<-p.doneCh
	return nil
}

func TestRuntimeManagerLazyStartAndMultiTaskReuse(t *testing.T) {
	var spawnCount atomic.Int32
	var stopCount atomic.Int32

	var lastProc *fakeManagedProcess
	var mu sync.Mutex
	fakeSrv := newFakeRuntimeServer("py-yolo-v2")

	starter := func(_ context.Context, _ agent.RuntimeSpec) (agent.ManagedProcess, *grpc.ClientConn, error) {
		spawnCount.Add(1)
		lis := bufconn.Listen(1024 * 1024)
		srv := grpc.NewServer()
		runtimev1.RegisterAlgorithmRuntimeServer(srv, fakeSrv)
		go func() { _ = srv.Serve(lis) }()

		proc := &fakeManagedProcess{
			lis:     lis,
			grpcSrv: srv,
			doneCh:  make(chan struct{}),
		}
		go func() {
			<-proc.doneCh
			stopCount.Add(1)
		}()

		conn, err := grpc.NewClient("passthrough:///fake-rt",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			_ = proc.Stop()
			return nil, nil, err
		}
		mu.Lock()
		lastProc = proc
		mu.Unlock()
		return proc, conn, nil
	}

	mgr := agent.NewRuntimeManager(agent.RuntimeManagerDeps{
		Specs: map[string]agent.RuntimeSpec{
			"py-yolo-v2": {
				RuntimeKey:      "py-yolo-v2",
				IdleGracePeriod: 120 * time.Millisecond,
				MaxTasks:        4,
			},
		},
		Starter: starter,
		Logger:  slog.Default(),
	})
	defer mgr.Shutdown()

	ctx := context.Background()

	// 1. Assign 2 tasks sharing the same runtime_key -> MUST spawn ONLY 1 Runtime process!
	if err := mgr.AssignTask(ctx, &runtimev1.RuntimeAssignTaskRequest{
		TaskId:          "task-1",
		CameraId:        "cam-1",
		StreamUrl:       "rtsp://mediamtx:8554/cam1?token=a",
		StreamProtocol:  "rtsp",
		LeaseId:         "lease-1",
		LeaseTtlSeconds: 30,
	}, "py-yolo-v2"); err != nil {
		t.Fatalf("AssignTask(task-1): %v", err)
	}
	if err := mgr.AssignTask(ctx, &runtimev1.RuntimeAssignTaskRequest{
		TaskId:          "task-2",
		CameraId:        "cam-2",
		StreamUrl:       "rtsp://mediamtx:8554/cam2?token=b",
		StreamProtocol:  "rtsp",
		LeaseId:         "lease-2",
		LeaseTtlSeconds: 30,
	}, "py-yolo-v2"); err != nil {
		t.Fatalf("AssignTask(task-2): %v", err)
	}

	if got := spawnCount.Load(); got != 1 {
		t.Fatalf("spawnCount = %d, want 1 (multi-task reuse on same runtime_key)", got)
	}

	// 2. Revoke both tasks -> after IdleGracePeriod (120ms), process MUST be reclaimed cleanly!
	if err := mgr.RevokeTask(ctx, "task-1", "test done"); err != nil {
		t.Fatalf("RevokeTask(task-1): %v", err)
	}
	if err := mgr.RevokeTask(ctx, "task-2", "test done"); err != nil {
		t.Fatalf("RevokeTask(task-2): %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p := lastProc
		mu.Unlock()
		if p != nil && p.stopped.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	stopped := lastProc != nil && lastProc.stopped.Load()
	mu.Unlock()
	if !stopped {
		t.Fatal("expected idle runtime process to be stopped after IdleGracePeriod elapsed")
	}
}
