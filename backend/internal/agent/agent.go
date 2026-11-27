package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	runtimev1 "aegisvision/backend/gen/go/runtime/v1"
)

// NodeAgentDeps configures a compute-host NodeAgent.
type NodeAgentDeps struct {
	NodeID            string
	Hostname          string
	IPAddr            string
	IngestAddr        string
	AgentToken        string
	AgentVersion      string
	MaxTasks          int
	HeartbeatInterval time.Duration
	RuntimeMgr        *RuntimeManager
	Buffer            *LocalBuffer
	Logger            *slog.Logger
}

// NodeAgent maintains outbound gRPC connectivity to aegis-ingest, orchestrates local Runtimes,
// and enforces split forwarding (in-memory transient live frames vs SQLite-buffered abnormal events).
type NodeAgent struct {
	nodeID            string
	hostname          string
	ipAddr            string
	ingestAddr        string
	agentToken        string
	agentVersion      string
	maxTasks          int
	heartbeatInterval time.Duration
	runtimeMgr        *RuntimeManager
	buffer            *LocalBuffer
	log               *slog.Logger

	online atomic.Bool
	liveCh chan *agentv1.DetectionFrame

	cmdMu sync.Mutex
}

// NewNodeAgent constructs a NodeAgent.
func NewNodeAgent(deps NodeAgentDeps) *NodeAgent {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.MaxTasks <= 0 {
		deps.MaxTasks = 8
	}
	if deps.HeartbeatInterval <= 0 {
		deps.HeartbeatInterval = 5 * time.Second
	}
	if deps.AgentVersion == "" {
		deps.AgentVersion = "0.1.0"
	}
	na := &NodeAgent{
		nodeID:            deps.NodeID,
		hostname:          deps.Hostname,
		ipAddr:            deps.IPAddr,
		ingestAddr:        deps.IngestAddr,
		agentToken:        deps.AgentToken,
		agentVersion:      deps.AgentVersion,
		maxTasks:          deps.MaxTasks,
		heartbeatInterval: deps.HeartbeatInterval,
		runtimeMgr:        deps.RuntimeMgr,
		buffer:            deps.Buffer,
		log:               deps.Logger,
		liveCh:            make(chan *agentv1.DetectionFrame, 64),
	}
	if na.runtimeMgr != nil {
		na.runtimeMgr.SetResultHandler(na.HandleRuntimeResult)
	}
	return na
}

// HandleRuntimeResult applies the strict split-forwarding rule:
// - Normal/heartbeat frames: in-memory drop-oldest live channel ONLY (never written to disk).
// - Abnormal/snapshot frames: sent live when connected; persisted to SQLite/WAL when offline.
func (a *NodeAgent) HandleRuntimeResult(ctx context.Context, res *runtimev1.RuntimeResult) {
	if res == nil {
		return
	}
	frame := runtimeResultToAgentFrame(res)
	abnormal := isAbnormalFrame(frame)

	if !a.online.Load() {
		if abnormal && a.buffer != nil {
			evt := &agentv1.BufferedEvent{
				EventId:    uuid.Must(uuid.NewV7()).String(),
				Frame:      frame,
				CachedAtMs: time.Now().UTC().UnixMilli(),
			}
			if err := a.buffer.Enqueue(ctx, evt); err != nil {
				a.log.Error("enqueue offline abnormal event failed", "err", err)
			}
		}
		// Normal frames while offline are intentionally dropped in-memory (no stale frame flood on reconnect).
		return
	}

	// Online path: enqueue onto bounded drop-oldest live frame channel.
	select {
	case a.liveCh <- frame:
	default:
		if abnormal && a.buffer != nil {
			// Never drop abnormal frames even under extreme live-stream backpressure: spill to WAL buffer.
			_ = a.buffer.Enqueue(ctx, &agentv1.BufferedEvent{
				EventId:    uuid.Must(uuid.NewV7()).String(),
				Frame:      frame,
				CachedAtMs: time.Now().UTC().UnixMilli(),
			})
			return
		}
		// Normal frame: evict oldest and keep latest.
		select {
		case <-a.liveCh:
		default:
		}
		select {
		case a.liveCh <- frame:
		default:
		}
	}
}

// FlushBufferedEvents replays all offline-buffered abnormal events over ReportBufferedEvents,
// deleting each record from SQLite ONLY after receiving an accepted EventAck.
func (a *NodeAgent) FlushBufferedEvents(ctx context.Context, client agentv1.AgentGatewayClient) error {
	if a.buffer == nil {
		return nil
	}

	outCtx := a.outgoingContext(ctx)
	for {
		batch, err := a.buffer.PeekBatch(ctx, 50)
		if err != nil {
			return fmt.Errorf("peek buffered events: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}

		stream, err := client.ReportBufferedEvents(outCtx)
		if err != nil {
			return fmt.Errorf("open ReportBufferedEvents stream: %w", err)
		}

		for _, evt := range batch {
			if err := stream.Send(evt); err != nil {
				_ = stream.CloseSend()
				return fmt.Errorf("send buffered event %s: %w", evt.GetEventId(), err)
			}
			ack, err := stream.Recv()
			if err != nil {
				_ = stream.CloseSend()
				return fmt.Errorf("recv event ack %s: %w", evt.GetEventId(), err)
			}
			if ack.GetAccepted() {
				if delErr := a.buffer.Delete(ctx, ack.GetEventId()); delErr != nil {
					a.log.Warn("delete acked buffered event failed", "event_id", ack.GetEventId(), "err", delErr)
				}
			} else {
				a.log.Warn("platform rejected buffered event", "event_id", ack.GetEventId(), "err", ack.GetErrorMessage())
			}
		}
		_ = stream.CloseSend()
	}
}

// Run maintains outbound gRPC connectivity to aegis-ingest with exponential backoff until ctx ends.
func (a *NodeAgent) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := a.runSession(ctx)
		a.online.Store(false)
		if ctx.Err() != nil {
			return nil
		}
		a.log.Warn("agent session disconnected; reconnecting", "backoff", backoff, "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (a *NodeAgent) runSession(ctx context.Context) error {
	conn, err := grpc.NewClient(a.ingestAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial ingest %q: %w", a.ingestAddr, err)
	}
	defer func() { _ = conn.Close() }()

	client := agentv1.NewAgentGatewayClient(conn)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outCtx := a.outgoingContext(sessionCtx)
	ctrl, err := client.ControlStream(outCtx)
	if err != nil {
		return fmt.Errorf("open ControlStream: %w", err)
	}

	var rtKeys []string
	if a.runtimeMgr != nil {
		rtKeys = a.runtimeMgr.RuntimeKeys()
	}
	if err := ctrl.Send(&agentv1.AgentUpstream{
		Payload: &agentv1.AgentUpstream_Register{
			Register: &agentv1.RegisterRequest{
				NodeId:       a.nodeID,
				Hostname:     a.hostname,
				IpAddr:       a.ipAddr,
				RuntimeKeys:  rtKeys,
				MaxTasks:     int32(a.maxTasks), // #nosec G115 -- task cap
				AgentVersion: a.agentVersion,
			},
		},
	}); err != nil {
		return fmt.Errorf("send RegisterRequest: %w", err)
	}

	a.online.Store(true)
	a.log.Info("connected to aegis-ingest", "node_id", a.nodeID, "ingest_addr", a.ingestAddr)

	// Drain any offline buffered abnormal events asynchronously on reconnect.
	go func() {
		if flushErr := a.FlushBufferedEvents(sessionCtx, client); flushErr != nil && sessionCtx.Err() == nil {
			a.log.Warn("flush buffered events on reconnect failed", "err", flushErr)
		}
	}()

	// Stream transient live frames while online.
	go a.liveFramePump(sessionCtx, client)

	// Periodic heartbeat loop.
	go func() {
		ticker := time.NewTicker(a.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				var statuses []*agentv1.RunningTaskStatus
				if a.runtimeMgr != nil {
					for _, st := range a.runtimeMgr.ActiveTaskStatuses() {
						statuses = append(statuses, &agentv1.RunningTaskStatus{
							TaskId:       st.TaskID,
							RuntimeKey:   st.RuntimeKey,
							ActualState:  st.State,
							ErrorMessage: st.ErrMsg,
						})
					}
				}
				a.cmdMu.Lock()
				sendErr := ctrl.Send(&agentv1.AgentUpstream{
					Payload: &agentv1.AgentUpstream_Heartbeat{
						Heartbeat: &agentv1.Heartbeat{
							NodeId:       a.nodeID,
							TimestampMs:  time.Now().UTC().UnixMilli(),
							RunningTasks: statuses,
						},
					},
				})
				a.cmdMu.Unlock()
				if sendErr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Receive platform downstream commands until stream closes.
	for {
		down, recvErr := ctrl.Recv()
		if errors.Is(recvErr, io.EOF) {
			return errors.New("control stream closed by server")
		}
		if recvErr != nil {
			return recvErr
		}
		a.handleDownstreamCommand(sessionCtx, ctrl, down)
	}
}

func (a *NodeAgent) liveFramePump(ctx context.Context, client agentv1.AgentGatewayClient) {
	stream, err := client.StreamLiveFrames(a.outgoingContext(ctx))
	if err != nil {
		return
	}
	defer func() { _, _ = stream.CloseAndRecv() }()

	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-a.liveCh:
			if sendErr := stream.Send(&agentv1.LiveFrameBatch{
				NodeId: a.nodeID,
				Frames: []*agentv1.DetectionFrame{frame},
			}); sendErr != nil {
				// If an abnormal frame failed to send right as the network dropped, save it to SQLite WAL!
				if isAbnormalFrame(frame) && a.buffer != nil {
					_ = a.buffer.Enqueue(context.Background(), &agentv1.BufferedEvent{
						EventId:    uuid.Must(uuid.NewV7()).String(),
						Frame:      frame,
						CachedAtMs: time.Now().UTC().UnixMilli(),
					})
				}
				return
			}
		}
	}
}

func (a *NodeAgent) handleDownstreamCommand(
	ctx context.Context,
	ctrl grpc.BidiStreamingClient[agentv1.AgentUpstream, agentv1.PlatformDownstream],
	down *agentv1.PlatformDownstream,
) {
	if a.runtimeMgr == nil || down == nil {
		return
	}

	sendAck := func(cmdID, taskID string, err error) {
		ack := &agentv1.TaskAck{
			CommandId: cmdID,
			TaskId:    taskID,
			Accepted:  err == nil,
		}
		if err != nil {
			ack.ErrorMessage = err.Error()
		}
		a.cmdMu.Lock()
		_ = ctrl.Send(&agentv1.AgentUpstream{
			Payload: &agentv1.AgentUpstream_TaskAck{TaskAck: ack},
		})
		a.cmdMu.Unlock()
	}

	switch p := down.GetPayload().(type) {
	case *agentv1.PlatformDownstream_AssignTask:
		cmd := p.AssignTask
		err := a.runtimeMgr.AssignTask(ctx, &runtimev1.RuntimeAssignTaskRequest{
			TaskId:             cmd.GetTaskId(),
			CameraId:           cmd.GetCameraId(),
			AlgorithmVersionId: cmd.GetAlgorithmVersionId(),
			StreamUrl:          cmd.GetStreamUrl(),
			StreamProtocol:     cmd.GetStreamProtocol(),
			Rois:               agentROIsToRuntime(cmd.GetRois()),
			AlgoParamsJson:     cmd.GetAlgoParamsJson(),
			LeaseId:            cmd.GetLeaseId(),
			LeaseTtlSeconds:    cmd.GetLeaseTtlSeconds(),
			ConfigVersion:      cmd.GetConfigVersion(),
		}, cmd.GetRuntimeKey())
		sendAck(cmd.GetCommandId(), cmd.GetTaskId(), err)

	case *agentv1.PlatformDownstream_UpdateTask:
		cmd := p.UpdateTask
		err := a.runtimeMgr.UpdateTask(ctx, &runtimev1.RuntimeUpdateTaskRequest{
			TaskId:         cmd.GetTaskId(),
			Rois:           agentROIsToRuntime(cmd.GetRois()),
			AlgoParamsJson: cmd.GetAlgoParamsJson(),
			ConfigVersion:  cmd.GetConfigVersion(),
		})
		sendAck(cmd.GetCommandId(), cmd.GetTaskId(), err)

	case *agentv1.PlatformDownstream_RevokeTask:
		cmd := p.RevokeTask
		err := a.runtimeMgr.RevokeTask(ctx, cmd.GetTaskId(), cmd.GetReason())
		sendAck(cmd.GetCommandId(), cmd.GetTaskId(), err)

	case *agentv1.PlatformDownstream_RenewLease:
		cmd := p.RenewLease
		if err := a.runtimeMgr.RenewLease(ctx, &runtimev1.RuntimeRenewLeaseRequest{
			TaskId:             cmd.GetTaskId(),
			LeaseId:            cmd.GetLeaseId(),
			LeaseTtlSeconds:    cmd.GetLeaseTtlSeconds(),
			RefreshedStreamUrl: cmd.GetRefreshedStreamUrl(),
		}); err != nil {
			a.log.Warn("forward RenewLease to runtime failed", "task_id", cmd.GetTaskId(), "err", err)
		}
	}
}

func (a *NodeAgent) outgoingContext(ctx context.Context) context.Context {
	pairs := []string{"x-node-id", a.nodeID}
	if a.agentToken != "" {
		pairs = append(pairs, "x-agent-token", a.agentToken)
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(pairs...))
}

func runtimeResultToAgentFrame(res *runtimev1.RuntimeResult) *agentv1.DetectionFrame {
	dets := make([]*agentv1.Detection, 0, len(res.GetDetections()))
	for _, d := range res.GetDetections() {
		dets = append(dets, &agentv1.Detection{
			Bbox:       d.GetBbox(),
			Label:      d.GetLabel(),
			Confidence: d.GetConfidence(),
			TrackId:    d.GetTrackId(),
			IsAbnormal: d.GetIsAbnormal(),
			Attributes: d.GetAttributes(),
		})
	}
	return &agentv1.DetectionFrame{
		TaskId:             res.GetTaskId(),
		CameraId:           res.GetCameraId(),
		AlgorithmVersionId: res.GetAlgorithmVersionId(),
		TimestampMs:        res.GetTimestampMs(),
		FrameWidth:         res.GetFrameWidth(),
		FrameHeight:        res.GetFrameHeight(),
		IsHeartbeat:        res.GetIsHeartbeat(),
		Detections:         dets,
		SnapshotJpeg:       res.GetSnapshotJpeg(),
	}
}

func isAbnormalFrame(f *agentv1.DetectionFrame) bool {
	if len(f.GetSnapshotJpeg()) > 0 {
		return true
	}
	for _, d := range f.GetDetections() {
		if d.GetIsAbnormal() {
			return true
		}
	}
	return false
}

func agentROIsToRuntime(rois []*agentv1.ROI) []*runtimev1.RuntimeROI {
	out := make([]*runtimev1.RuntimeROI, 0, len(rois))
	for _, r := range rois {
		pts := make([]*runtimev1.RuntimePoint, 0, len(r.GetPoints()))
		for _, pt := range r.GetPoints() {
			pts = append(pts, &runtimev1.RuntimePoint{X: pt.GetX(), Y: pt.GetY()})
		}
		out = append(out, &runtimev1.RuntimeROI{
			RoiId:   r.GetRoiId(),
			Name:    r.GetName(),
			RoiType: r.GetRoiType(),
			Points:  pts,
		})
	}
	return out
}
