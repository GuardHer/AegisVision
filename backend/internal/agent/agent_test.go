package agent_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	runtimev1 "aegisvision/backend/gen/go/runtime/v1"
	"aegisvision/backend/internal/agent"
)

type spyIngestGateway struct {
	agentv1.UnimplementedAgentGatewayServer

	mu             sync.Mutex
	liveFrames     []*agentv1.DetectionFrame
	bufferedEvents []*agentv1.BufferedEvent
}

func (s *spyIngestGateway) ControlStream(stream grpc.BidiStreamingServer[agentv1.AgentUpstream, agentv1.PlatformDownstream]) error {
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) || err != nil {
			return nil
		}
	}
}

func (s *spyIngestGateway) StreamLiveFrames(stream grpc.ClientStreamingServer[agentv1.LiveFrameBatch, agentv1.StreamAck]) error {
	var count int64
	for {
		batch, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&agentv1.StreamAck{ReceivedFrames: count})
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		for _, f := range batch.GetFrames() {
			s.liveFrames = append(s.liveFrames, f)
			count++
		}
		s.mu.Unlock()
	}
}

func (s *spyIngestGateway) ReportBufferedEvents(stream grpc.BidiStreamingServer[agentv1.BufferedEvent, agentv1.EventAck]) error {
	for {
		evt, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.bufferedEvents = append(s.bufferedEvents, evt)
		s.mu.Unlock()
		if err := stream.Send(&agentv1.EventAck{EventId: evt.GetEventId(), Accepted: true}); err != nil {
			return err
		}
	}
}

// Review Focus #2 (Agent side): During platform disconnect, 50 normal live frames must NOT be buffered to disk,
// while 2 abnormal snapshot frames MUST be written to local SQLite/WAL, replayed on reconnect, and deleted after EventAck.
func TestAgentSplitForwardingLiveDropVsAbnormalDiskBuffer(t *testing.T) {
	buf, err := agent.NewLocalBuffer(filepath.Join(t.TempDir(), "offline.sqlite"))
	if err != nil {
		t.Fatalf("NewLocalBuffer(): %v", err)
	}
	defer func() { _ = buf.Close() }()

	na := agent.NewNodeAgent(agent.NodeAgentDeps{
		NodeID:     "gpu-node-test",
		Hostname:   "gpu-host",
		IPAddr:     "192.168.0.50",
		AgentToken: "tok",
		Buffer:     buf,
		Logger:     slog.Default(),
	})

	ctx := context.Background()

	// 1. Platform is OFFLINE: feed 50 normal frames + 2 abnormal snapshot frames
	for i := 0; i < 50; i++ {
		na.HandleRuntimeResult(ctx, &runtimev1.RuntimeResult{
			TaskId:      "t-1",
			CameraId:    "cam-1",
			TimestampMs: time.Now().UnixMilli(),
			Detections: []*runtimev1.RuntimeDetection{
				{Bbox: []float32{0.1, 0.1, 0.2, 0.5}, Label: "person", Confidence: 0.9, IsAbnormal: false},
			},
		})
	}
	for i := 0; i < 2; i++ {
		na.HandleRuntimeResult(ctx, &runtimev1.RuntimeResult{
			TaskId:      "t-1",
			CameraId:    "cam-1",
			TimestampMs: time.Now().UnixMilli(),
			Detections: []*runtimev1.RuntimeDetection{
				{Bbox: []float32{0.2, 0.2, 0.5, 0.8}, Label: "no_helmet", Confidence: 0.95, IsAbnormal: true},
			},
			SnapshotJpeg: []byte{0xFF, 0xD8, byte(i + 1), 0xFF, 0xD9},
		})
	}

	// Verify ONLY the 2 abnormal frames hit SQLite disk (0 normal live frames written to disk!)
	diskCount, err := buf.Count(ctx)
	if err != nil {
		t.Fatalf("buf.Count(): %v", err)
	}
	if diskCount != 2 {
		t.Fatalf("offline SQLite buffer count = %d, want 2 (50 normal frames must NOT touch disk)", diskCount)
	}

	// 2. Platform comes ONLINE: replay buffered events via ReportBufferedEvents
	lis := bufconn.Listen(1024 * 1024)
	spyGW := &spyIngestGateway{}
	grpcSrv := grpc.NewServer()
	agentv1.RegisterAgentGatewayServer(grpcSrv, spyGW)
	go func() { _ = grpcSrv.Serve(lis) }()
	defer func() {
		grpcSrv.Stop()
		_ = lis.Close()
	}()

	conn, err := grpc.NewClient("passthrough:///buf-ingest",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial fake ingest: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := na.FlushBufferedEvents(ctx, agentv1.NewAgentGatewayClient(conn)); err != nil {
		t.Fatalf("FlushBufferedEvents(): %v", err)
	}

	// Verify 2 abnormal events replayed, 0 normal history frames replayed, and SQLite buffer cleared
	spyGW.mu.Lock()
	replayedCount := len(spyGW.bufferedEvents)
	liveCount := len(spyGW.liveFrames)
	spyGW.mu.Unlock()

	if replayedCount != 2 {
		t.Fatalf("replayed buffered events = %d, want 2", replayedCount)
	}
	if liveCount != 0 {
		t.Fatalf("expected 0 historical normal frames replayed, got %d", liveCount)
	}
	remaining, err := buf.Count(ctx)
	if err != nil || remaining != 0 {
		t.Fatalf("expected SQLite buffer empty after EventAck, got (%d, %v)", remaining, err)
	}
}
