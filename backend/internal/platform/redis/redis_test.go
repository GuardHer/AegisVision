package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	platformredis "github.com/GuardHer/AegisVision/backend/internal/platform/redis"
)

func newTestRedisClient(t *testing.T) *platformredis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if addr := os.Getenv("AEGIS_TEST_REDIS_ADDR"); addr != "" {
		c, err := platformredis.New(ctx, addr, os.Getenv("AEGIS_TEST_REDIS_PASSWORD"), 0)
		if err != nil {
			t.Fatalf("platformredis.New(live) failed: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	mr := miniredis.RunT(t)
	c, err := platformredis.New(ctx, mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("platformredis.New(miniredis) failed: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSchedulerLeaderMutualExclusion(t *testing.T) {
	c := newTestRedisClient(t)
	ctx := context.Background()

	// Ensure clean state if running against live Redis
	_ = c.ReleaseSchedulerLeader(ctx, "sched-a")
	_ = c.ReleaseSchedulerLeader(ctx, "sched-b")
	t.Cleanup(func() {
		_ = c.ReleaseSchedulerLeader(context.Background(), "sched-a")
		_ = c.ReleaseSchedulerLeader(context.Background(), "sched-b")
	})

	acquiredA, err := c.AcquireSchedulerLeader(ctx, "sched-a", 5*time.Second)
	if err != nil || !acquiredA {
		t.Fatalf("sched-a acquire = (%v, %v), want (true, nil)", acquiredA, err)
	}

	acquiredB, err := c.AcquireSchedulerLeader(ctx, "sched-b", 5*time.Second)
	if err != nil || acquiredB {
		t.Fatalf("sched-b acquire while held by sched-a = (%v, %v), want (false, nil)", acquiredB, err)
	}

	renewedA, err := c.RenewSchedulerLeader(ctx, "sched-a", 5*time.Second)
	if err != nil || !renewedA {
		t.Fatalf("sched-a renew = (%v, %v), want (true, nil)", renewedA, err)
	}

	// sched-b must NOT be able to release sched-a's lock
	if err := c.ReleaseSchedulerLeader(ctx, "sched-b"); err != nil {
		t.Fatalf("sched-b release unexpected error: %v", err)
	}
	acquiredBAgain, err := c.AcquireSchedulerLeader(ctx, "sched-b", 5*time.Second)
	if err != nil || acquiredBAgain {
		t.Fatalf("sched-b should still fail acquire after wrong-owner release: (%v, %v)", acquiredBAgain, err)
	}

	// Now sched-a releases, sched-b can take over
	if err := c.ReleaseSchedulerLeader(ctx, "sched-a"); err != nil {
		t.Fatalf("sched-a release failed: %v", err)
	}
	acquiredBNow, err := c.AcquireSchedulerLeader(ctx, "sched-b", 5*time.Second)
	if err != nil || !acquiredBNow {
		t.Fatalf("sched-b acquire after release = (%v, %v), want (true, nil)", acquiredBNow, err)
	}
}

func TestStreamTokensAgentRouteAndPubSub(t *testing.T) {
	c := newTestRedisClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stream token round-trip
	tok := "tok-test-123"
	wantPayload := platformredis.StreamTokenPayload{
		UserID:     "user-1",
		StreamPath: "cam_001",
	}
	if err := c.SetStreamToken(ctx, platformredis.StreamTokenKindPlay, tok, wantPayload, 30*time.Second); err != nil {
		t.Fatalf("SetStreamToken() failed: %v", err)
	}
	gotPayload, err := c.GetStreamToken(ctx, platformredis.StreamTokenKindPlay, tok)
	if err != nil {
		t.Fatalf("GetStreamToken() failed: %v", err)
	}
	if gotPayload.UserID != wantPayload.UserID || gotPayload.StreamPath != wantPayload.StreamPath {
		t.Fatalf("got token payload %+v, want %+v", gotPayload, wantPayload)
	}

	// Agent route
	if err := c.SetAgentRoute(ctx, "node-01", "ingest-02", 30*time.Second); err != nil {
		t.Fatalf("SetAgentRoute() failed: %v", err)
	}
	gotIngest, err := c.GetAgentRoute(ctx, "node-01")
	if err != nil || gotIngest != "ingest-02" {
		t.Fatalf("GetAgentRoute() = (%q, %v), want (ingest-02, nil)", gotIngest, err)
	}

	// Pub/Sub frames
	msgCh, unsub, err := c.SubscribeFrames(ctx, "cam-pubsub-1")
	if err != nil {
		t.Fatalf("SubscribeFrames() failed: %v", err)
	}
	defer func() { _ = unsub() }()

	if err := c.PublishFrame(ctx, "cam-pubsub-1", []byte(`{"detections":[]}`)); err != nil {
		t.Fatalf("PublishFrame() failed: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.CameraID != "cam-pubsub-1" || string(msg.Payload) != `{"detections":[]}` {
			t.Fatalf("unexpected pubsub message: %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Redis Pub/Sub frame message")
	}
}
