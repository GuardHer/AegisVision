package agent_test

import (
	"context"
	"path/filepath"
	"testing"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/agent"
)

func TestLocalBufferSQLiteDurabilityAndAckCleanup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent-buffer.sqlite")
	ctx := context.Background()

	buf, err := agent.NewLocalBuffer(dbPath)
	if err != nil {
		t.Fatalf("NewLocalBuffer(): %v", err)
	}

	ev1 := &agentv1.BufferedEvent{
		EventId:    "evt-1",
		CachedAtMs: 1001,
		Frame: &agentv1.DetectionFrame{
			TaskId:       "t-1",
			CameraId:     "c-1",
			SnapshotJpeg: []byte{0xFF, 0xD8, 0x01, 0xFF, 0xD9},
		},
	}
	ev2 := &agentv1.BufferedEvent{
		EventId:    "evt-2",
		CachedAtMs: 1002,
		Frame: &agentv1.DetectionFrame{
			TaskId:       "t-1",
			CameraId:     "c-1",
			SnapshotJpeg: []byte{0xFF, 0xD8, 0x02, 0xFF, 0xD9},
		},
	}

	if err := buf.Enqueue(ctx, ev1); err != nil {
		t.Fatalf("Enqueue(ev1): %v", err)
	}
	if err := buf.Enqueue(ctx, ev2); err != nil {
		t.Fatalf("Enqueue(ev2): %v", err)
	}
	if err := buf.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	// Reopen from disk to prove WAL persistence
	buf2, err := agent.NewLocalBuffer(dbPath)
	if err != nil {
		t.Fatalf("reopen NewLocalBuffer(): %v", err)
	}
	defer func() { _ = buf2.Close() }()

	items, err := buf2.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch(): %v", err)
	}
	if len(items) != 2 || items[0].GetEventId() != "evt-1" || items[1].GetEventId() != "evt-2" {
		t.Fatalf("unexpected persisted items: %+v", items)
	}

	// Ack evt-1 -> only evt-2 remains
	if err := buf2.Delete(ctx, "evt-1"); err != nil {
		t.Fatalf("Delete(evt-1): %v", err)
	}
	count, err := buf2.Count(ctx)
	if err != nil || count != 1 {
		t.Fatalf("Count() = (%d, %v), want (1, nil)", count, err)
	}
}
