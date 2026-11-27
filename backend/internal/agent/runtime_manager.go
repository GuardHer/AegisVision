package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	runtimev1 "aegisvision/backend/gen/go/runtime/v1"
)

// RuntimeSpec defines how aegis-agent launches a local Python Runtime process for a runtime_key.
type RuntimeSpec struct {
	RuntimeKey      string            `yaml:"runtime_key"`
	Command         string            `yaml:"command"`
	Args            []string          `yaml:"args"`
	WorkDir         string            `yaml:"work_dir"`
	Env             map[string]string `yaml:"env"`
	MaxTasks        int               `yaml:"max_tasks"`
	IdleGracePeriod time.Duration     `yaml:"idle_grace_period"`
}

// ManagedProcess abstracts a running local Runtime OS process (for production & testability).
type ManagedProcess interface {
	Address() string
	Stop() error
	Wait() error
}

// ProcessStarter spawns a local Runtime process and returns its handle and gRPC connection.
type ProcessStarter func(ctx context.Context, spec RuntimeSpec) (ManagedProcess, *grpc.ClientConn, error)

// ResultHandler receives streamed inference results and empty heartbeats from local Runtimes.
type ResultHandler func(ctx context.Context, res *runtimev1.RuntimeResult)

// RuntimeManagerDeps configures RuntimeManager.
type RuntimeManagerDeps struct {
	Specs     map[string]RuntimeSpec
	Starter   ProcessStarter
	OnResult  ResultHandler
	Logger    *slog.Logger
}

// ActiveTaskInfo snapshots the live status of a task running on a local Runtime.
type ActiveTaskInfo struct {
	TaskID     string
	RuntimeKey string
	State      string
	ErrMsg     string
}

type runtimeInstance struct {
	spec       RuntimeSpec
	proc       ManagedProcess
	conn       *grpc.ClientConn
	client     runtimev1.AlgorithmRuntimeClient
	tasks      map[string]*runtimev1.RuntimeAssignTaskRequest
	streamStop context.CancelFunc
	idleTimer  *time.Timer
}

// RuntimeManager lazily launches, reuses, monitors, and idly reclaims local Runtime processes by runtime_key.
type RuntimeManager struct {
	specs    map[string]RuntimeSpec
	starter  ProcessStarter
	onResult ResultHandler
	log      *slog.Logger

	mu         sync.Mutex
	instances  map[string]*runtimeInstance // runtime_key -> running instance
	taskToKey  map[string]string           // task_id -> runtime_key
	taskStates map[string]ActiveTaskInfo
	closed     bool
}

// NewRuntimeManager constructs a RuntimeManager.
func NewRuntimeManager(deps RuntimeManagerDeps) *RuntimeManager {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Starter == nil {
		deps.Starter = DefaultProcessStarter
	}
	specs := make(map[string]RuntimeSpec, len(deps.Specs))
	for k, v := range deps.Specs {
		if v.RuntimeKey == "" {
			v.RuntimeKey = k
		}
		if v.IdleGracePeriod <= 0 {
			v.IdleGracePeriod = 60 * time.Second
		}
		specs[k] = v
	}
	return &RuntimeManager{
		specs:      specs,
		starter:    deps.Starter,
		onResult:   deps.OnResult,
		log:        deps.Logger,
		instances:  make(map[string]*runtimeInstance),
		taskToKey:  make(map[string]string),
		taskStates: make(map[string]ActiveTaskInfo),
	}
}

// RuntimeKeys returns all configured runtime_key values on this host.
func (m *RuntimeManager) RuntimeKeys() []string {
	keys := make([]string, 0, len(m.specs))
	for k := range m.specs {
		keys = append(keys, k)
	}
	return keys
}

// SetResultHandler registers the callback invoked for every RuntimeResult.
func (m *RuntimeManager) SetResultHandler(fn ResultHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResult = fn
}

// AssignTask ensures the Runtime for runtimeKey is running (reusing if already started) and assigns req.
func (m *RuntimeManager) AssignTask(ctx context.Context, req *runtimev1.RuntimeAssignTaskRequest, runtimeKey string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("runtime manager is closed")
	}
	spec, ok := m.specs[runtimeKey]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown runtime_key %q on this node", runtimeKey)
	}

	inst, running := m.instances[runtimeKey]
	if running && inst.idleTimer != nil {
		inst.idleTimer.Stop()
		inst.idleTimer = nil
	}
	m.mu.Unlock()

	if !running {
		var err error
		inst, err = m.startInstance(ctx, spec)
		if err != nil {
			return err
		}
	}

	reply, err := inst.client.AssignTask(ctx, req)
	if err != nil {
		return fmt.Errorf("runtime %q AssignTask(%s): %w", runtimeKey, req.GetTaskId(), err)
	}
	if !reply.GetAccepted() {
		return fmt.Errorf("runtime %q rejected task %s: %s", runtimeKey, req.GetTaskId(), reply.GetErrorMessage())
	}

	m.mu.Lock()
	inst.tasks[req.GetTaskId()] = req
	m.taskToKey[req.GetTaskId()] = runtimeKey
	m.taskStates[req.GetTaskId()] = ActiveTaskInfo{
		TaskID:     req.GetTaskId(),
		RuntimeKey: runtimeKey,
		State:      "running",
	}
	m.mu.Unlock()
	return nil
}

// UpdateTask pushes updated ROIs / algorithm parameters to an active task's Runtime.
func (m *RuntimeManager) UpdateTask(ctx context.Context, req *runtimev1.RuntimeUpdateTaskRequest) error {
	inst, err := m.instanceForTask(req.GetTaskId())
	if err != nil {
		return err
	}
	reply, err := inst.client.UpdateTask(ctx, req)
	if err != nil {
		return err
	}
	if !reply.GetAccepted() {
		return fmt.Errorf("runtime rejected UpdateTask(%s): %s", req.GetTaskId(), reply.GetErrorMessage())
	}
	return nil
}

// RevokeTask stops a task on its Runtime and schedules idle process reclamation if active tasks reach 0.
func (m *RuntimeManager) RevokeTask(ctx context.Context, taskID, reason string) error {
	m.mu.Lock()
	runtimeKey, ok := m.taskToKey[taskID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	inst, exists := m.instances[runtimeKey]
	m.mu.Unlock()

	if exists {
		_, err := inst.client.RevokeTask(ctx, &runtimev1.RuntimeRevokeTaskRequest{
			TaskId: taskID,
			Reason: reason,
		})
		if err != nil {
			m.log.Warn("runtime RevokeTask error", "runtime_key", runtimeKey, "task_id", taskID, "err", err)
		}
	}

	m.mu.Lock()
	delete(m.taskToKey, taskID)
	delete(m.taskStates, taskID)
	if exists {
		delete(inst.tasks, taskID)
		if len(inst.tasks) == 0 && inst.idleTimer == nil {
			grace := inst.spec.IdleGracePeriod
			inst.idleTimer = time.AfterFunc(grace, func() {
				m.reclaimIfIdle(runtimeKey, inst)
			})
		}
	}
	m.mu.Unlock()
	return nil
}

// RenewLease forwards a lease renewal (and optional refreshed RTSP URL) to the task's Runtime.
func (m *RuntimeManager) RenewLease(ctx context.Context, req *runtimev1.RuntimeRenewLeaseRequest) error {
	inst, err := m.instanceForTask(req.GetTaskId())
	if err != nil {
		return err
	}
	reply, err := inst.client.RenewLease(ctx, req)
	if err != nil {
		return err
	}
	if !reply.GetAccepted() {
		return fmt.Errorf("runtime rejected RenewLease(%s): %s", req.GetTaskId(), reply.GetErrorMessage())
	}
	return nil
}

// ActiveTaskStatuses returns snapshots of all active tasks for Agent heartbeats.
func (m *RuntimeManager) ActiveTaskStatuses() []ActiveTaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ActiveTaskInfo, 0, len(m.taskStates))
	for _, st := range m.taskStates {
		out = append(out, st)
	}
	return out
}

// Shutdown gracefully stops every managed Runtime process and closes all local gRPC connections.
func (m *RuntimeManager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	toStop := make([]*runtimeInstance, 0, len(m.instances))
	for k, inst := range m.instances {
		if inst.idleTimer != nil {
			inst.idleTimer.Stop()
			inst.idleTimer = nil
		}
		toStop = append(toStop, inst)
		delete(m.instances, k)
	}
	m.mu.Unlock()

	for _, inst := range toStop {
		m.stopInstance(inst)
	}
}

func (m *RuntimeManager) instanceForTask(taskID string) (*runtimeInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtimeKey, ok := m.taskToKey[taskID]
	if !ok {
		return nil, fmt.Errorf("task %q not found on this agent", taskID)
	}
	inst, ok := m.instances[runtimeKey]
	if !ok {
		return nil, fmt.Errorf("runtime %q not running", runtimeKey)
	}
	return inst, nil
}

func (m *RuntimeManager) startInstance(ctx context.Context, spec RuntimeSpec) (*runtimeInstance, error) {
	proc, conn, err := m.starter(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("start runtime %q: %w", spec.RuntimeKey, err)
	}
	client := runtimev1.NewAlgorithmRuntimeClient(conn)

	// Wait for Runtime gRPC Health() readiness
	readyCtx, cancelReady := context.WithTimeout(ctx, 15*time.Second)
	defer cancelReady()
	if err := waitForRuntimeHealth(readyCtx, client); err != nil {
		_ = conn.Close()
		_ = proc.Stop()
		return nil, fmt.Errorf("runtime %q health check failed: %w", spec.RuntimeKey, err)
	}

	streamCtx, cancelStream := context.WithCancel(context.Background())
	inst := &runtimeInstance{
		spec:       spec,
		proc:       proc,
		conn:       conn,
		client:     client,
		tasks:      make(map[string]*runtimev1.RuntimeAssignTaskRequest),
		streamStop: cancelStream,
	}

	m.mu.Lock()
	// Handle rare race where another goroutine started the same runtime_key simultaneously
	if existing, already := m.instances[spec.RuntimeKey]; already {
		m.mu.Unlock()
		m.stopInstance(inst)
		return existing, nil
	}
	m.instances[spec.RuntimeKey] = inst
	m.mu.Unlock()

	go m.consumeResults(streamCtx, inst)
	m.log.Info("started local runtime process", "runtime_key", spec.RuntimeKey, "addr", proc.Address())
	return inst, nil
}

func (m *RuntimeManager) consumeResults(ctx context.Context, inst *runtimeInstance) {
	stream, err := inst.client.StreamResults(ctx, &runtimev1.StreamResultsRequest{
		RuntimeKey: inst.spec.RuntimeKey,
	})
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("runtime StreamResults open failed", "runtime_key", inst.spec.RuntimeKey, "err", err)
		}
		return
	}
	for {
		res, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) || ctx.Err() != nil {
			return
		}
		if recvErr != nil {
			m.log.Warn("runtime StreamResults error", "runtime_key", inst.spec.RuntimeKey, "err", recvErr)
			return
		}
		m.mu.Lock()
		handler := m.onResult
		m.mu.Unlock()
		if handler != nil {
			handler(ctx, res)
		}
	}
}

func (m *RuntimeManager) reclaimIfIdle(runtimeKey string, target *runtimeInstance) {
	m.mu.Lock()
	cur, ok := m.instances[runtimeKey]
	if !ok || cur != target || len(cur.tasks) > 0 {
		m.mu.Unlock()
		return
	}
	delete(m.instances, runtimeKey)
	cur.idleTimer = nil
	m.mu.Unlock()

	m.log.Info("reclaiming idle runtime process to free GPU resources", "runtime_key", runtimeKey)
	m.stopInstance(cur)
}

func (m *RuntimeManager) stopInstance(inst *runtimeInstance) {
	if inst.streamStop != nil {
		inst.streamStop()
	}
	if inst.conn != nil {
		_ = inst.conn.Close()
	}
	if inst.proc != nil {
		_ = inst.proc.Stop()
	}
}

func waitForRuntimeHealth(ctx context.Context, client runtimev1.AlgorithmRuntimeClient) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		checkCtx, cancel := context.WithTimeout(ctx, time.Second)
		resp, err := client.Health(checkCtx, &runtimev1.HealthRequest{})
		cancel()
		if err == nil && resp.GetReady() {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return err
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type osManagedProcess struct {
	addr string
	cmd  *exec.Cmd
}

func (p *osManagedProcess) Address() string { return p.addr }

func (p *osManagedProcess) Stop() error {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (p *osManagedProcess) Wait() error {
	if p.cmd == nil {
		return nil
	}
	return p.cmd.Wait()
}

// DefaultProcessStarter allocates a loopback TCP port, spawns the configured Python Runtime process
// with AEGIS_RUNTIME_ADDR and AEGIS_RUNTIME_KEY environment variables, and dials it over local gRPC.
func DefaultProcessStarter(_ context.Context, spec RuntimeSpec) (ManagedProcess, *grpc.ClientConn, error) {
	if spec.Command == "" {
		return nil, nil, fmt.Errorf("runtime %q missing launch command", spec.RuntimeKey)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("allocate local runtime port: %w", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	// #nosec G204 -- command/args come from operator-configured agent.yaml runtime registry
	cmd := exec.Command(spec.Command, spec.Args...)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = append(os.Environ(),
		"AEGIS_RUNTIME_ADDR="+addr,
		"AEGIS_RUNTIME_KEY="+spec.RuntimeKey,
	)
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("launch runtime %q: %w", spec.RuntimeKey, err)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, fmt.Errorf("dial local runtime %q at %s: %w", spec.RuntimeKey, addr, err)
	}

	return &osManagedProcess{addr: addr, cmd: cmd}, conn, nil
}
