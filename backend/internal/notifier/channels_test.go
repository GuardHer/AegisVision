package notifier_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/notifier"
)

type resettableSpyStore struct {
	alarmCtx   notifier.AlarmContext
	deliveries []notifier.DeliveryRecord
	chType     string
	chConfig   []byte
}

func (s *resettableSpyStore) GetAlarmContext(_ context.Context, _ uuid.UUID) (notifier.AlarmContext, error) {
	return s.alarmCtx, nil
}
func (s *resettableSpyStore) ListDeliveriesForAlarm(_ context.Context, _ uuid.UUID) ([]notifier.DeliveryRecord, error) {
	return s.deliveries, nil
}
func (s *resettableSpyStore) GetDelivery(_ context.Context, _ uuid.UUID) (notifier.DeliveryRecord, error) {
	return s.deliveries[0], nil
}
func (s *resettableSpyStore) UpdateDeliveryResult(_ context.Context, _ notifier.DeliveryUpdate) error {
	return nil
}
func (s *resettableSpyStore) GetChannelConfig(_ context.Context, _ uuid.UUID) (string, []byte, error) {
	return s.chType, s.chConfig, nil
}

func TestWeComDingTalkAndSoundLightNotifiersAndReset(t *testing.T) {
	sample := notifier.Notification{
		AlarmID:     "alarm-1",
		TriggeredAt: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC),
		Severity:    "critical",
		RuleMode:    "anomaly_detect",
		Title:       "Smoking Detected",
		Camera:      notifier.CameraRef{ID: "c1", Name: "Workshop-Cam-1"},
		ROI:         notifier.ROIRef{ID: "r1", Name: "ZoneA"},
		Evidence:    notifier.EvidenceRef{ImageURL: "http://example/img.jpg"},
	}

	// 1. WeCom Markdown test
	var wecomBody string
	wecomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		wecomBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer wecomSrv.Close()

	wecom := notifier.NewWeComNotifier(wecomSrv.Client())
	cfgWeCom, _ := json.Marshal(notifier.WeComConfig{WebhookURL: wecomSrv.URL})
	if res, err := wecom.Send(context.Background(), cfgWeCom, sample); err != nil || res.StatusCode != 200 {
		t.Fatalf("wecom Send failed: res=%+v err=%v", res, err)
	}
	if !strings.Contains(wecomBody, `"msgtype":"markdown"`) || !strings.Contains(wecomBody, "Smoking Detected") {
		t.Fatalf("unexpected wecom body: %s", wecomBody)
	}

	// 2. Review Focus #3: DingTalk HMAC-SHA256 Base64 query signing
	var gotTS, gotSign, dingBody string
	dingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTS = r.URL.Query().Get("timestamp")
		gotSign = r.URL.Query().Get("sign")
		b, _ := io.ReadAll(r.Body)
		dingBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer dingSrv.Close()

	ding := notifier.NewDingTalkNotifier(dingSrv.Client())
	cfgDing, _ := json.Marshal(notifier.DingTalkConfig{WebhookURL: dingSrv.URL + "/robot/send?access_token=abc", Secret: "SEC_TEST_KEY"})
	if _, err := ding.Send(context.Background(), cfgDing, sample); err != nil {
		t.Fatalf("dingtalk Send failed: %v", err)
	}
	tsInt, err := strconv.ParseInt(gotTS, 10, 64)
	if err != nil || gotSign != notifier.ComputeDingTalkSign("SEC_TEST_KEY", tsInt) {
		t.Fatalf("dingtalk HMAC signature mismatch: ts=%q sign=%q", gotTS, gotSign)
	}
	if !strings.Contains(dingBody, "Smoking Detected") {
		t.Fatalf("unexpected dingtalk body: %s", dingBody)
	}

	// 3. Review Focus #3: SoundLight trigger + auto_reset_seconds + manual reset_channel via Dispatcher
	var triggerHits, resetHits atomic.Int32
	slSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/trigger":
			triggerHits.Add(1)
		case "/reset":
			resetHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slSrv.Close()

	chID := uuid.Must(uuid.NewV7())
	alarmID := uuid.Must(uuid.NewV7())
	cfgSL, _ := json.Marshal(notifier.SoundLightConfig{
		TriggerURL:       slSrv.URL + "/trigger",
		ResetURL:         slSrv.URL + "/reset",
		BodyTemplate:     `{"cmd":"on","title":"{{title}}"}`,
		AutoResetSeconds: 1,
	})

	reg := notifier.NewRegistry()
	reg.Register(notifier.NewSoundLightNotifier(slSrv.Client()))
	bgCtx, cancelBg := context.WithCancel(context.Background())
	defer cancelBg()

	store := &resettableSpyStore{
		alarmCtx: notifier.AlarmContext{AlarmID: alarmID, Severity: "critical", RuleMode: "anomaly_detect", Title: "Fire"},
		deliveries: []notifier.DeliveryRecord{
			{ID: uuid.Must(uuid.NewV7()), AlarmID: alarmID, ChannelID: chID, ChannelType: "sound_light", ChannelConfig: cfgSL, MaxAttempts: 3},
		},
		chType:   "sound_light",
		chConfig: cfgSL,
	}
	disp := notifier.NewDispatcher(notifier.DispatcherDeps{
		Context:  bgCtx,
		Registry: reg,
		Store:    store,
	})

	// Trigger alarm -> triggers sound_light AND schedules 1s auto_reset
	job, _ := json.Marshal(map[string]any{"alarm_id": alarmID.String()})
	if err := disp.HandleMessage(context.Background(), job); err != nil {
		t.Fatalf("HandleMessage trigger: %v", err)
	}
	if triggerHits.Load() != 1 {
		t.Fatalf("expected 1 sound_light trigger hit, got %d", triggerHits.Load())
	}

	// Manual reset_channel command -> immediately calls /reset AND cancels pending auto_reset timer.
	resetJob, _ := json.Marshal(map[string]any{"action": "reset_channel", "channel_id": chID.String()})
	if err := disp.HandleMessage(context.Background(), resetJob); err != nil {
		t.Fatalf("HandleMessage reset_channel: %v", err)
	}
	if resetHits.Load() != 1 {
		t.Fatalf("expected immediate manual reset hit=1, got %d", resetHits.Load())
	}

	// Re-trigger alarm without manual reset -> 1s auto_reset timer MUST fire /reset (2nd reset hit).
	if err := disp.HandleMessage(context.Background(), job); err != nil {
		t.Fatalf("HandleMessage second trigger: %v", err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) && resetHits.Load() < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	if resetHits.Load() < 2 {
		t.Fatalf("expected auto_reset_seconds timer to fire /reset, got total resetHits=%d", resetHits.Load())
	}
}
