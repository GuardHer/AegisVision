package ws_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/ws"
)

func TestWSHubCameraSubscriptionIsolation(t *testing.T) {
	hub := ws.NewHub(ws.HubConfig{
		FrameBufferSize: 8,
		AlarmBufferSize: 8,
	})

	clientA := hub.Register("client-a")
	defer hub.Unregister("client-a")
	clientB := hub.Register("client-b")
	defer hub.Unregister("client-b")

	hub.SetSubscriptions("client-a", []string{"cam-1"})
	hub.SetSubscriptions("client-b", []string{"cam-2"})

	hub.BroadcastFrame("cam-1", []byte(`{"type":"frame_detections","camera_id":"cam-1","seq":1}`))
	hub.BroadcastFrame("cam-2", []byte(`{"type":"frame_detections","camera_id":"cam-2","seq":2}`))
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"a-1"}`))

	// Client A receives only cam-1 frame
	select {
	case msg := <-clientA.FrameChan():
		if !strings.Contains(string(msg), `"cam-1"`) {
			t.Fatalf("client-a got unexpected frame: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("client-a timed out waiting for cam-1 frame")
	}
	select {
	case extra := <-clientA.FrameChan():
		t.Fatalf("client-a received cross-camera frame: %s", extra)
	default:
	}

	// Client B receives only cam-2 frame
	select {
	case msg := <-clientB.FrameChan():
		if !strings.Contains(string(msg), `"cam-2"`) {
			t.Fatalf("client-b got unexpected frame: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("client-b timed out waiting for cam-2 frame")
	}

	// Both clients receive global alarm_alert
	for _, tc := range []struct {
		name string
		ch   <-chan []byte
	}{
		{"client-a", clientA.AlarmChan()},
		{"client-b", clientB.AlarmChan()},
	} {
		select {
		case alert := <-tc.ch:
			if !strings.Contains(string(alert), `"a-1"`) {
				t.Fatalf("%s got unexpected alarm: %s", tc.name, alert)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive global alarm_alert", tc.name)
		}
	}
}

// Review Focus #5: Slow WS client must drop oldest frames without blocking fast clients or leaking memory,
// while alarm_alert remains protected on its dedicated priority channel.
func TestWSHubSlowClientDropsOldFramesWithoutBlockingFastClient(t *testing.T) {
	hub := ws.NewHub(ws.HubConfig{
		FrameBufferSize: 4,
		AlarmBufferSize: 4,
	})

	slowClient := hub.Register("slow-a")
	defer hub.Unregister("slow-a")
	fastClient := hub.Register("fast-b")
	defer hub.Unregister("fast-b")

	hub.SetSubscriptions("slow-a", []string{"cam-1"})
	hub.SetSubscriptions("fast-b", []string{"cam-1"})

	fastReceived := make([]string, 0, 100)
	doneFast := make(chan struct{})
	go func() {
		defer close(doneFast)
		for i := 0; i < 100; i++ {
			select {
			case msg := <-fastClient.FrameChan():
				fastReceived = append(fastReceived, string(msg))
			case <-time.After(2 * time.Second):
				return
			}
		}
	}()

	// Push 100 rapid frames while slowClient never reads
	start := time.Now()
	for i := 1; i <= 100; i++ {
		payload := fmt.Sprintf(`{"type":"frame_detections","camera_id":"cam-1","seq":%d}`, i)
		hub.BroadcastFrame("cam-1", []byte(payload))
		time.Sleep(500 * time.Microsecond)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("BroadcastFrame blocked by slow client: took %v", elapsed)
	}

	// Send priority alarm alert while slow client's frame buffer is completely saturated
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"priority-1"}`))

	<-doneFast
	if len(fastReceived) == 0 || !strings.Contains(fastReceived[len(fastReceived)-1], `"seq":100`) {
		t.Fatalf("fast client missed latest frame; got count=%d", len(fastReceived))
	}

	// Slow client's frame buffer is strictly bounded to 4 and must retain the newest frames (seq 97..100)
	if bufLen := len(slowClient.FrameChan()); bufLen > 4 {
		t.Fatalf("slow client frame buffer grew unbounded: len=%d, want <= 4", bufLen)
	}
	var lastSlow string
	for len(slowClient.FrameChan()) > 0 {
		lastSlow = string(<-slowClient.FrameChan())
	}
	if !strings.Contains(lastSlow, `"seq":100`) {
		t.Fatalf("slow client did not preserve newest frame (seq:100); last=%s", lastSlow)
	}

	// Priority alarm must still be intact for slowClient
	select {
	case alert := <-slowClient.AlarmChan():
		if !strings.Contains(string(alert), `"priority-1"`) {
			t.Fatalf("unexpected priority alarm: %s", alert)
		}
	default:
		t.Fatal("priority alarm was dropped or missing on slow client")
	}
}

func TestWSServerAuthSubscribeAndReceive(t *testing.T) {
	tm := auth.NewTokenManager("ws-jwt-secret-must-be-16-chars", time.Hour)
	hub := ws.NewHub(ws.HubConfig{FrameBufferSize: 8, AlarmBufferSize: 8})
	srv := ws.NewServer(ws.ServerDeps{
		Hub:    hub,
		Tokens: tm,
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/ws/realtime"

	// 1. Unauthenticated connection must fail
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		t.Fatal("expected unauthenticated WS dial to fail")
	} else if resp != nil && resp.StatusCode != 401 {
		t.Errorf("unauthenticated WS status = %d, want 401", resp.StatusCode)
	}

	// 2. Authenticated connection subscribes and receives frame
	tok, _, _, _ := tm.IssueAccessToken("u-1", "viewer", domain.RoleViewer)
	conn, _, err := websocket.Dial(ctx, wsURL+"?token="+tok, nil)
	if err != nil {
		t.Fatalf("authenticated WS dial failed: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	subMsg, _ := json.Marshal(map[string]any{
		"type":       "subscribe",
		"camera_ids": []string{"cam-live-1"},
	})
	if err := conn.Write(ctx, websocket.MessageText, subMsg); err != nil {
		t.Fatalf("write subscribe msg: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	hub.BroadcastFrame("cam-live-1", []byte(`{"type":"frame_detections","camera_id":"cam-live-1","roi_counts":{"r1":2}}`))

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	if !strings.Contains(string(data), `"cam-live-1"`) {
		t.Fatalf("unexpected ws message: %s", data)
	}
}

// Review Focus #1: Restricted workshop WS client MUST NOT receive unauthorized frames or alarms,
// and MUST be immediately evicted from active camera subscriptions upon UpdateUserScope revocation.
func TestWSHubWorkshopScopeFilteringAndInstantRevocation(t *testing.T) {
	hub := ws.NewHub(ws.HubConfig{FrameBufferSize: 8, AlarmBufferSize: 8})

	scopeA := ws.ClientScope{
		UserID:         "user-ws-a",
		Unrestricted:   false,
		AllowedZones:   map[string]struct{}{"zone-a": {}},
		AllowedCameras: map[string]struct{}{"cam-a": {}},
	}
	clientA := hub.RegisterWithScope("sess-a", scopeA)
	defer hub.Unregister("sess-a")

	// Attempting to subscribe to both cam-a (allowed) and cam-b (unauthorized) MUST drop cam-b.
	hub.SetSubscriptions("sess-a", []string{"cam-a", "cam-b"})

	hub.BroadcastFrame("cam-a", []byte(`{"type":"frame_detections","camera_id":"cam-a"}`))
	hub.BroadcastFrame("cam-b", []byte(`{"type":"frame_detections","camera_id":"cam-b"}`))

	select {
	case msg := <-clientA.FrameChan():
		if !strings.Contains(string(msg), `"cam-a"`) {
			t.Fatalf("expected cam-a frame, got %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing authorized cam-a frame")
	}
	select {
	case leaked := <-clientA.FrameChan():
		t.Fatalf("unauthorized cam-b frame leaked to restricted client: %s", leaked)
	default:
	}

	// Alarms MUST be filtered by zone_id (even if camera_id was previously in AllowedCameras before a move!).
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"a-ok","zone_id":"zone-a","camera_id":"cam-a"}`))
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"a-leak","zone_id":"zone-b","camera_id":"cam-b"}`))
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"a-moved-cam-leak","zone_id":"zone-b","camera_id":"cam-a"}`))

	select {
	case msg := <-clientA.AlarmChan():
		if !strings.Contains(string(msg), `"a-ok"`) {
			t.Fatalf("expected a-ok alarm, got %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing authorized alarm")
	}
	select {
	case leaked := <-clientA.AlarmChan():
		t.Fatalf("unauthorized zone-b alarm leaked: %s", leaked)
	default:
	}

	// Instant online revocation: admin removes zone-a from user-ws-a while connected.
	hub.UpdateUserScope("user-ws-a", ws.ClientScope{
		UserID:         "user-ws-a",
		Unrestricted:   false,
		AllowedZones:   map[string]struct{}{},
		AllowedCameras: map[string]struct{}{},
	})

	hub.BroadcastFrame("cam-a", []byte(`{"type":"frame_detections","camera_id":"cam-a","after_revoke":true}`))
	hub.BroadcastAlarm([]byte(`{"type":"alarm_alert","alarm_id":"a-after-revoke","zone_id":"zone-a","camera_id":"cam-a"}`))

	select {
	case leaked := <-clientA.FrameChan():
		t.Fatalf("frame leaked after instant scope revocation: %s", leaked)
	default:
	}
	select {
	case leaked := <-clientA.AlarmChan():
		t.Fatalf("alarm leaked after instant scope revocation: %s", leaked)
	default:
	}
}
