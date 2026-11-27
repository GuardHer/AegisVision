package notifier_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/notifier"
)

func TestWebhookNotifierPayloadAndHMACSignature(t *testing.T) {
	const secret = "wh-hmac-secret-2026"

	var gotHeaders http.Header
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := notifier.NewWebhookNotifier(srv.Client())
	rawCfg, _ := json.Marshal(map[string]any{
		"url":                   srv.URL,
		"method":                "POST",
		"headers":               map[string]string{"X-Custom-Source": "plant-a"},
		"secret":                secret,
		"timeout_ms":            3000,
		"include_evidence_urls": true,
	})
	if err := wh.ValidateConfig(rawCfg); err != nil {
		t.Fatalf("ValidateConfig() failed: %v", err)
	}

	signer := auth.NewEvidenceSigner("evidence-secret-1234567890")
	alarmID := uuid.Must(uuid.NewV7()).String()
	deliveryID := uuid.Must(uuid.NewV7()).String()
	now := time.Unix(1795700000, 0).UTC()

	res, err := wh.Send(context.Background(), rawCfg, notifier.Notification{
		DeliveryID:  deliveryID,
		AlarmID:     alarmID,
		TriggeredAt: now,
		Severity:    domain.SeverityCritical,
		RuleMode:    domain.RuleModeAbsenceDetect,
		Title:       "Station 1 absence > 60s",
		Camera:      notifier.CameraRef{ID: "cam-1", Name: "Press Workshop 1", Location: "Zone A"},
		ROI:         notifier.ROIRef{ID: "roi-1", Name: "Duty Station 1"},
		Algorithm:   notifier.AlgorithmRef{Key: "person_detect", Version: "v1.2.0"},
		Evidence: notifier.EvidenceRef{
			ImageURL:    signer.SignEvidenceURL("http://192.168.0.114", alarmID, "image", 24*time.Hour),
			VideoStatus: domain.VideoStatusRecording,
			VideoURL:    signer.SignEvidenceURL("http://192.168.0.114", alarmID, "video", 24*time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("WebhookNotifier.Send() failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", res.StatusCode)
	}

	// Verify headers and HMAC-SHA256 signature formula: sha256=<hex(HMAC_SHA256(secret, timestamp + "." + raw_body))>
	if gotHeaders.Get("X-Aegis-Event") != "alarm.triggered" {
		t.Errorf("X-Aegis-Event = %q, want alarm.triggered", gotHeaders.Get("X-Aegis-Event"))
	}
	if gotHeaders.Get("X-Aegis-Delivery-Id") != deliveryID {
		t.Errorf("X-Aegis-Delivery-Id = %q, want %q", gotHeaders.Get("X-Aegis-Delivery-Id"), deliveryID)
	}
	if gotHeaders.Get("X-Custom-Source") != "plant-a" {
		t.Errorf("X-Custom-Source = %q, want plant-a", gotHeaders.Get("X-Custom-Source"))
	}

	ts := gotHeaders.Get("X-Aegis-Timestamp")
	sig := gotHeaders.Get("X-Aegis-Signature")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(ts + "." + string(gotBody)))
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != wantSig {
		t.Fatalf("X-Aegis-Signature = %q, want %q", sig, wantSig)
	}

	var parsed map[string]any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("invalid json payload: %v", err)
	}
	evidenceMap, _ := parsed["evidence"].(map[string]any)
	imgURL, _ := evidenceMap["image_url"].(string)
	if !strings.Contains(imgURL, "/api/v1/evidence/"+alarmID+"/image?exp=") || !strings.Contains(imgURL, "&sig=") {
		t.Fatalf("expected signed image_url in payload, got %q", imgURL)
	}
}

type spyDeliveryStore struct {
	mu         sync.Mutex
	alarm      notifier.AlarmContext
	deliveries map[uuid.UUID]notifier.DeliveryRecord
}

func (s *spyDeliveryStore) GetAlarmContext(_ context.Context, _ uuid.UUID) (notifier.AlarmContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alarm, nil
}

func (s *spyDeliveryStore) ListDeliveriesForAlarm(_ context.Context, _ uuid.UUID) ([]notifier.DeliveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]notifier.DeliveryRecord, 0, len(s.deliveries))
	for _, d := range s.deliveries {
		out = append(out, d)
	}
	return out, nil
}

func (s *spyDeliveryStore) GetDelivery(_ context.Context, id uuid.UUID) (notifier.DeliveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliveries[id], nil
}

func (s *spyDeliveryStore) UpdateDeliveryResult(_ context.Context, res notifier.DeliveryUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.deliveries[res.DeliveryID]
	rec.Status = res.Status
	rec.Attempts = res.Attempts
	rec.LastStatusCode = res.LastStatusCode
	rec.LastLatencyMs = res.LastLatencyMs
	rec.LastError = res.LastError
	s.deliveries[res.DeliveryID] = rec
	return nil
}

type retryRecord struct {
	FailedAttempts int
	Body           []byte
}

type spyRetryBus struct {
	mu      sync.Mutex
	retries []retryRecord
}

func (b *spyRetryBus) PublishAlarmRetry(_ context.Context, failedAttempts int, body []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retries = append(b.retries, retryRecord{
		FailedAttempts: failedAttempts,
		Body:           append([]byte(nil), body...),
	})
	return nil
}

// Review Focus #5: Hanging webhook channel A must time out and route to DLX retry without blocking fast channel B.
func TestWebhookTimeoutRoutesToDLXWithoutBlockingOtherChannel(t *testing.T) {
	hangRelease := make(chan struct{})
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hangRelease:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(hangRelease)
		slowSrv.Close()
	}()

	fastReceivedAt := make(chan time.Time, 1)
	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fastReceivedAt <- time.Now()
		w.WriteHeader(http.StatusOK)
	}))
	defer fastSrv.Close()

	alarmID := uuid.Must(uuid.NewV7())
	delSlowID := uuid.Must(uuid.NewV7())
	delFastID := uuid.Must(uuid.NewV7())
	chSlowID := uuid.Must(uuid.NewV7())
	chFastID := uuid.Must(uuid.NewV7())

	slowCfg, _ := json.Marshal(map[string]any{"url": slowSrv.URL, "timeout_ms": 150})
	fastCfg, _ := json.Marshal(map[string]any{"url": fastSrv.URL, "timeout_ms": 1000})

	store := &spyDeliveryStore{
		alarm: notifier.AlarmContext{
			AlarmID:     alarmID,
			TriggeredAt: time.Now().UTC(),
			Severity:    domain.SeverityCritical,
			RuleMode:    domain.RuleModeAnomalyDetect,
			Title:       "No Helmet",
			Camera:      notifier.CameraRef{ID: "cam-1", Name: "Line 1"},
		},
		deliveries: map[uuid.UUID]notifier.DeliveryRecord{
			delSlowID: {
				ID:            delSlowID,
				AlarmID:       alarmID,
				ChannelID:     chSlowID,
				ChannelType:   "webhook",
				ChannelConfig: slowCfg,
				Status:        "pending",
				Attempts:      0,
				MaxAttempts:   4,
			},
			delFastID: {
				ID:            delFastID,
				AlarmID:       alarmID,
				ChannelID:     chFastID,
				ChannelType:   "webhook",
				ChannelConfig: fastCfg,
				Status:        "pending",
				Attempts:      0,
				MaxAttempts:   4,
			},
		},
	}

	retryBus := &spyRetryBus{}
	reg := notifier.NewRegistry()
	reg.Register(notifier.NewWebhookNotifier(nil))

	disp := notifier.NewDispatcher(notifier.DispatcherDeps{
		Registry:      reg,
		Store:         store,
		RetryBus:      retryBus,
		Signer:        auth.NewEvidenceSigner("evidence-sign-secret-1234567890"),
		PublicBaseURL: "http://192.168.0.114",
		EvidenceTTL:   24 * time.Hour,
		Logger:        slog.Default(),
	})

	start := time.Now()
	jobRaw, _ := json.Marshal(map[string]string{"alarm_id": alarmID.String()})
	if err := disp.HandleMessage(context.Background(), jobRaw); err != nil {
		t.Fatalf("Dispatcher.HandleMessage() failed: %v", err)
	}

	// Fast channel B must have received the webhook well before slow channel A timed out
	select {
	case ts := <-fastReceivedAt:
		if ts.Sub(start) > 120*time.Millisecond {
			t.Errorf("fast channel B was delayed by slow channel A: elapsed=%v", ts.Sub(start))
		}
	default:
		t.Fatal("fast channel B never received webhook")
	}

	store.mu.Lock()
	fastRec := store.deliveries[delFastID]
	slowRec := store.deliveries[delSlowID]
	store.mu.Unlock()

	if fastRec.Status != "success" || fastRec.Attempts != 1 || fastRec.LastStatusCode != http.StatusOK {
		t.Errorf("unexpected fast channel B record: %+v", fastRec)
	}
	if slowRec.Status != "failed" || slowRec.Attempts != 1 || slowRec.LastError == "" {
		t.Errorf("unexpected slow channel A record: %+v", slowRec)
	}

	retryBus.mu.Lock()
	defer retryBus.mu.Unlock()
	if len(retryBus.retries) != 1 || retryBus.retries[0].FailedAttempts != 1 {
		t.Fatalf("expected 1 DLX retry (attempt=1 -> alarms.retry.3s), got %+v", retryBus.retries)
	}
}
