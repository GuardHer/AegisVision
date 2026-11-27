package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
)

const agentRouteTTL = 30 * time.Second

// RouteRegistry records which ingest instance holds each online Agent's stream.
type RouteRegistry interface {
	SetAgentRoute(ctx context.Context, nodeID, ingestInstanceID string, ttl time.Duration) error
}

// GatewayDeps holds explicit dependencies for the AgentGateway gRPC server.
type GatewayDeps struct {
	InstanceID string
	AgentToken string
	Repo       Repository
	Redis      RouteRegistry
	Processor  *Processor
	Logger     *slog.Logger
}

// Gateway implements agentv1.AgentGatewayServer and routes scheduler commands to connected Agents.
type Gateway struct {
	agentv1.UnimplementedAgentGatewayServer

	instanceID string
	agentToken string
	repo       Repository
	redis      RouteRegistry
	processor  *Processor
	log        *slog.Logger

	mu      sync.RWMutex
	streams map[string]chan *agentv1.PlatformDownstream
}

// NewGateway creates an AgentGateway gRPC server instance.
func NewGateway(deps GatewayDeps) *Gateway {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Gateway{
		instanceID: deps.InstanceID,
		agentToken: deps.AgentToken,
		repo:       deps.Repo,
		redis:      deps.Redis,
		processor:  deps.Processor,
		log:        deps.Logger,
		streams:    make(map[string]chan *agentv1.PlatformDownstream),
	}
}

func (g *Gateway) authenticateMetadata(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if g.agentToken != "" {
		vals := md.Get("x-agent-token")
		if len(vals) == 0 || vals[0] != g.agentToken {
			return "", status.Error(codes.Unauthenticated, "invalid x-agent-token")
		}
	}
	nodeID := ""
	if vals := md.Get("x-node-id"); len(vals) > 0 {
		nodeID = vals[0]
	}
	return nodeID, nil
}

// ControlStream handles bidirectional Agent registration, heartbeats, task ACKs, and downstream commands.
func (g *Gateway) ControlStream(stream grpc.BidiStreamingServer[agentv1.AgentUpstream, agentv1.PlatformDownstream]) error {
	ctx := stream.Context()
	nodeID, err := g.authenticateMetadata(ctx)
	if err != nil {
		return err
	}

	sendCh := make(chan *agentv1.PlatformDownstream, 64)
	registeredNode := ""
	registerStream := func(id string) {
		if id == "" || id == registeredNode {
			return
		}
		g.mu.Lock()
		if registeredNode != "" {
			delete(g.streams, registeredNode)
		}
		g.streams[id] = sendCh
		registeredNode = id
		g.mu.Unlock()
	}
	if nodeID != "" {
		registerStream(nodeID)
	}
	defer func() {
		if registeredNode != "" {
			g.mu.Lock()
			if cur, ok := g.streams[registeredNode]; ok && cur == sendCh {
				delete(g.streams, registeredNode)
			}
			g.mu.Unlock()
		}
	}()

	writeCtx, cancelWrite := context.WithCancel(ctx)
	defer cancelWrite()

	writeErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-writeCtx.Done():
				writeErrCh <- nil
				return
			case msg := <-sendCh:
				if sendErr := stream.Send(msg); sendErr != nil {
					writeErrCh <- sendErr
					return
				}
			}
		}
	}()

	for {
		up, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			cancelWrite()
			<-writeErrCh
			return nil
		}
		if recvErr != nil {
			cancelWrite()
			<-writeErrCh
			return recvErr
		}

		switch p := up.GetPayload().(type) {
		case *agentv1.AgentUpstream_Register:
			reg := p.Register
			registerStream(reg.GetNodeId())
			if g.repo != nil {
				metaBytes, _ := json.Marshal(map[string]any{"agent_version": reg.GetAgentVersion()})
				if err := g.repo.UpsertAgentNode(ctx, NodeRegistration{
					NodeID:      reg.GetNodeId(),
					Hostname:    reg.GetHostname(),
					IPAddr:      reg.GetIpAddr(),
					RuntimeKeys: reg.GetRuntimeKeys(),
					MaxTasks:    int(reg.GetMaxTasks()),
					Meta:        metaBytes,
				}); err != nil {
					g.log.Warn("upsert agent node failed", "node_id", reg.GetNodeId(), "err", err)
				}
			}
			if g.redis != nil && reg.GetNodeId() != "" {
				if err := g.redis.SetAgentRoute(ctx, reg.GetNodeId(), g.instanceID, agentRouteTTL); err != nil {
					g.log.Warn("set agent route failed", "node_id", reg.GetNodeId(), "err", err)
				}
			}

		case *agentv1.AgentUpstream_Heartbeat:
			hb := p.Heartbeat
			registerStream(hb.GetNodeId())
			if g.redis != nil && hb.GetNodeId() != "" {
				_ = g.redis.SetAgentRoute(ctx, hb.GetNodeId(), g.instanceID, agentRouteTTL)
			}
			if g.repo != nil {
				var metaBytes []byte
				if hm := hb.GetHostMetrics(); hm != nil {
					metaBytes, _ = json.Marshal(map[string]any{
						"cpu_percent":        hm.GetCpuPercent(),
						"memory_percent":     hm.GetMemoryPercent(),
						"gpu_percent":        hm.GetGpuPercent(),
						"gpu_memory_percent": hm.GetGpuMemoryPercent(),
						"has_gpu":            hm.GetHasGpu(),
						"gpu_temp_c":         hm.GetGpuTempC(),
					})
				} else {
					metaBytes = []byte(`{}`)
				}
				_ = g.repo.UpdateAgentHeartbeat(ctx, hb.GetNodeId(), len(hb.GetRunningTasks()), metaBytes)
				for _, t := range hb.GetRunningTasks() {
					_ = g.repo.UpdateTaskState(ctx, t.GetTaskId(), t.GetActualState(), t.GetErrorMessage())
				}
			}

		case *agentv1.AgentUpstream_TaskAck:
			ack := p.TaskAck
			if g.repo != nil {
				state := "running"
				if !ack.GetAccepted() {
					state = "failed"
				}
				_ = g.repo.UpdateTaskState(ctx, ack.GetTaskId(), state, ack.GetErrorMessage())
			}
		}
	}
}

// StreamLiveFrames ingests high-frequency transient detection frames.
func (g *Gateway) StreamLiveFrames(stream grpc.ClientStreamingServer[agentv1.LiveFrameBatch, agentv1.StreamAck]) error {
	ctx := stream.Context()
	if _, err := g.authenticateMetadata(ctx); err != nil {
		return err
	}

	var count int64
	for {
		batch, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&agentv1.StreamAck{ReceivedFrames: count})
		}
		if err != nil {
			return err
		}
		for _, frame := range batch.GetFrames() {
			count++
			if g.processor != nil {
				if procErr := g.processor.ProcessLiveFrame(ctx, frame); procErr != nil {
					g.log.Warn("process live frame error", "task_id", frame.GetTaskId(), "err", procErr)
				}
			}
		}
	}
}

// ReportBufferedEvents ingests offline-cached abnormal events with event_id idempotency.
func (g *Gateway) ReportBufferedEvents(stream grpc.BidiStreamingServer[agentv1.BufferedEvent, agentv1.EventAck]) error {
	ctx := stream.Context()
	if _, err := g.authenticateMetadata(ctx); err != nil {
		return err
	}

	for {
		evt, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		ack := &agentv1.EventAck{
			EventId:  evt.GetEventId(),
			Accepted: true,
		}
		if g.processor != nil {
			if procErr := g.processor.ProcessBufferedEvent(ctx, evt); procErr != nil {
				ack.Accepted = false
				ack.ErrorMessage = procErr.Error()
			}
		}
		if sendErr := stream.Send(ack); sendErr != nil {
			return sendErr
		}
	}
}

// routedCommandEnvelope mirrors the RabbitMQ aegis.control JSON contract without importing internal/scheduler.
type routedCommandEnvelope struct {
	CommandID   string `json:"command_id"`
	NodeID      string `json:"node_id"`
	CommandType string `json:"command_type"`
	AssignTask  *struct {
		TaskID             string `json:"task_id"`
		CameraID           string `json:"camera_id"`
		AlgorithmVersionID string `json:"algorithm_version_id"`
		RuntimeKey         string `json:"runtime_key"`
		StreamURL          string `json:"stream_url"`
		StreamProtocol     string `json:"stream_protocol"`
		ROIs               []struct {
			ROIID   string          `json:"roi_id"`
			Name    string          `json:"name"`
			ROIType string          `json:"roi_type"`
			Points  json.RawMessage `json:"points"`
		} `json:"rois"`
		AlgoParamsJSON  string `json:"algo_params_json"`
		LeaseID         string `json:"lease_id"`
		LeaseTTLSeconds int32  `json:"lease_ttl_seconds"`
		ConfigVersion   int64  `json:"config_version"`
	} `json:"assign_task"`
	UpdateTask *struct {
		TaskID string `json:"task_id"`
		ROIs   []struct {
			ROIID   string          `json:"roi_id"`
			Name    string          `json:"name"`
			ROIType string          `json:"roi_type"`
			Points  json.RawMessage `json:"points"`
		} `json:"rois"`
		AlgoParamsJSON string `json:"algo_params_json"`
		ConfigVersion  int64  `json:"config_version"`
	} `json:"update_task"`
	RevokeTask *struct {
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	} `json:"revoke_task"`
	RenewLease *struct {
		TaskID             string `json:"task_id"`
		LeaseID            string `json:"lease_id"`
		LeaseTTLSeconds    int32  `json:"lease_ttl_seconds"`
		RefreshedStreamURL string `json:"refreshed_stream_url"`
	} `json:"renew_lease"`
}

// HandleRoutedCommand translates a RabbitMQ aegis.control message into a PlatformDownstream protobuf
// and pushes it onto the target Agent's active ControlStream.
func (g *Gateway) HandleRoutedCommand(_ context.Context, raw []byte) error {
	var env routedCommandEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("unmarshal routed command: %w", err)
	}

	var down *agentv1.PlatformDownstream
	switch env.CommandType {
	case "assign_task":
		if env.AssignTask == nil {
			return nil
		}
		pbROIs := make([]*agentv1.ROI, 0, len(env.AssignTask.ROIs))
		for _, r := range env.AssignTask.ROIs {
			pbROIs = append(pbROIs, toProtoROI(r.ROIID, r.Name, r.ROIType, r.Points))
		}
		down = &agentv1.PlatformDownstream{
			Payload: &agentv1.PlatformDownstream_AssignTask{
				AssignTask: &agentv1.AssignTaskCommand{
					CommandId:          env.CommandID,
					TaskId:             env.AssignTask.TaskID,
					CameraId:           env.AssignTask.CameraID,
					AlgorithmVersionId: env.AssignTask.AlgorithmVersionID,
					RuntimeKey:         env.AssignTask.RuntimeKey,
					StreamUrl:          env.AssignTask.StreamURL,
					StreamProtocol:     env.AssignTask.StreamProtocol,
					Rois:               pbROIs,
					AlgoParamsJson:     env.AssignTask.AlgoParamsJSON,
					LeaseId:            env.AssignTask.LeaseID,
					LeaseTtlSeconds:    env.AssignTask.LeaseTTLSeconds,
					ConfigVersion:      env.AssignTask.ConfigVersion,
				},
			},
		}
	case "update_task":
		if env.UpdateTask == nil {
			return nil
		}
		pbROIs := make([]*agentv1.ROI, 0, len(env.UpdateTask.ROIs))
		for _, r := range env.UpdateTask.ROIs {
			pbROIs = append(pbROIs, toProtoROI(r.ROIID, r.Name, r.ROIType, r.Points))
		}
		down = &agentv1.PlatformDownstream{
			Payload: &agentv1.PlatformDownstream_UpdateTask{
				UpdateTask: &agentv1.UpdateTaskCommand{
					CommandId:      env.CommandID,
					TaskId:         env.UpdateTask.TaskID,
					Rois:           pbROIs,
					AlgoParamsJson: env.UpdateTask.AlgoParamsJSON,
					ConfigVersion:  env.UpdateTask.ConfigVersion,
				},
			},
		}
	case "revoke_task":
		if env.RevokeTask == nil {
			return nil
		}
		down = &agentv1.PlatformDownstream{
			Payload: &agentv1.PlatformDownstream_RevokeTask{
				RevokeTask: &agentv1.RevokeTaskCommand{
					CommandId: env.CommandID,
					TaskId:    env.RevokeTask.TaskID,
					Reason:    env.RevokeTask.Reason,
				},
			},
		}
	case "renew_lease":
		if env.RenewLease == nil {
			return nil
		}
		down = &agentv1.PlatformDownstream{
			Payload: &agentv1.PlatformDownstream_RenewLease{
				RenewLease: &agentv1.RenewLeaseCommand{
					TaskId:             env.RenewLease.TaskID,
					LeaseId:            env.RenewLease.LeaseID,
					LeaseTtlSeconds:    env.RenewLease.LeaseTTLSeconds,
					RefreshedStreamUrl: env.RenewLease.RefreshedStreamURL,
				},
			},
		}
	default:
		return nil
	}

	g.mu.RLock()
	ch, online := g.streams[env.NodeID]
	g.mu.RUnlock()
	if !online {
		g.log.Warn("target agent node not connected to this ingest instance", "node_id", env.NodeID, "command_type", env.CommandType)
		return nil
	}

	select {
	case ch <- down:
		return nil
	default:
		return fmt.Errorf("agent %q downstream command buffer full", env.NodeID)
	}
}

func toProtoROI(id, name, roiType string, rawPoints json.RawMessage) *agentv1.ROI {
	var pts []struct {
		X float32 `json:"x"`
		Y float32 `json:"y"`
	}
	_ = json.Unmarshal(rawPoints, &pts)
	pbPts := make([]*agentv1.Point, 0, len(pts))
	for _, pt := range pts {
		pbPts = append(pbPts, &agentv1.Point{X: pt.X, Y: pt.Y})
	}
	return &agentv1.ROI{
		RoiId:   id,
		Name:    name,
		RoiType: roiType,
		Points:  pbPts,
	}
}
