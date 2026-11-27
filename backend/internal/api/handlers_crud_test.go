package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/platform/telemetry"
)

type spyEventPublisher struct {
	mu           sync.Mutex
	configEvents [][]byte
	alarmEvents  [][]byte
}

func (s *spyEventPublisher) PublishConfigEvent(_ context.Context, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configEvents = append(s.configEvents, append([]byte(nil), body...))
	return nil
}

func (s *spyEventPublisher) PublishAlarm(_ context.Context, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alarmEvents = append(s.alarmEvents, append([]byte(nil), body...))
	return nil
}

type memStreamAndAuthCache struct {
	mu          sync.Mutex
	tokens      map[string]platformredis.StreamTokenPayload
	blacklisted map[string]bool
}

func newMemCache() *memStreamAndAuthCache {
	return &memStreamAndAuthCache{
		tokens:      make(map[string]platformredis.StreamTokenPayload),
		blacklisted: make(map[string]bool),
	}
}

func (m *memStreamAndAuthCache) SetStreamToken(_ context.Context, kind, token string, payload platformredis.StreamTokenPayload, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[kind+":"+token] = payload
	return nil
}

func (m *memStreamAndAuthCache) GetStreamToken(_ context.Context, kind, token string) (*platformredis.StreamTokenPayload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.tokens[kind+":"+token]
	if !ok {
		return nil, platformredis.ErrKeyNotFound
	}
	return &p, nil
}

func (m *memStreamAndAuthCache) BlacklistToken(_ context.Context, jti string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blacklisted[jti] = true
	return nil
}

func (m *memStreamAndAuthCache) IsTokenBlacklisted(_ context.Context, jti string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blacklisted[jti], nil
}

func setupAPITestRouter(t *testing.T) (*gin.Engine, *postgres.Store, *spyEventPublisher, *auth.TokenManager) {
	t.Helper()
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("AEGIS_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("AEGIS_TEST_POSTGRES_DSN not set; skipping API CRUD integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New(): %v", err)
	}
	t.Cleanup(func() { store.Close() })

	localObj, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal(): %v", err)
	}
	pub := &spyEventPublisher{}
	cache := newMemCache()
	tm := auth.NewTokenManager("test-jwt-secret-1234567890", time.Hour)
	signer := auth.NewEvidenceSigner("test-sign-secret-1234567890")
	mtx := mediamtx.New("http://127.0.0.1:9997", "rtsp://127.0.0.1:8554", "/whep", nil)
	health := telemetry.NewHealth("aegis-api-test")
	health.SetReady(true)

	router := api.NewRouter(api.RouterDeps{
		Store:           store,
		Cache:           cache,
		Publisher:       pub,
		Storage:         localObj,
		MediaMTX:        mtx,
		Tokens:          tm,
		Signer:          signer,
		Health:          health,
		Logger:          slog.Default(),
		RefreshTokenTTL: 24 * time.Hour,
	})
	return router, store, pub, tm
}

func doJSONReq(t *testing.T, r http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode json body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestValidateROIPointsNormalized(t *testing.T) {
	if err := api.ValidateROIPoints(domain.ROITypePolygon, []api.PointInput{
		{X: 0.1, Y: 0.1},
		{X: 0.9, Y: 0.1},
		{X: 0.5, Y: 0.9},
	}); err != nil {
		t.Fatalf("expected valid polygon to pass, got: %v", err)
	}
	if err := api.ValidateROIPoints(domain.ROITypePolygon, []api.PointInput{
		{X: -0.1, Y: 0.1},
		{X: 0.9, Y: 0.1},
		{X: 0.5, Y: 0.9},
	}); err == nil {
		t.Fatal("expected out-of-range x=-0.1 to be rejected")
	}
	if err := api.ValidateROIPoints(domain.ROITypePolygon, []api.PointInput{
		{X: 0.1, Y: 1.2},
		{X: 0.9, Y: 0.1},
		{X: 0.5, Y: 0.9},
	}); err == nil {
		t.Fatal("expected out-of-range y=1.2 to be rejected")
	}
	if err := api.ValidateROIPoints(domain.ROITypePolygon, []api.PointInput{
		{X: 0.1, Y: 0.2},
		{X: 0.5, Y: 0.5},
	}); err == nil {
		t.Fatal("expected polygon with < 3 points to be rejected")
	}
	if err := api.ValidateROIPoints(domain.ROITypeFullFrame, nil); err != nil {
		t.Fatalf("expected full_frame with empty points to pass, got: %v", err)
	}
}

func TestCameraROIRuleCRUDAndConfigEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, store, pub, tm := setupAPITestRouter(t)

	_, adminTok := seedTestUserToken(t, store, tm, domain.RoleAdmin)

	var createdCamID, createdAlgoID, createdVerID, createdROIID, createdRuleID string
	t.Cleanup(func() {
		ctx := context.Background()
		if createdRuleID != "" {
			_, _ = store.Pool().Exec(ctx, "DELETE FROM roi_algorithm_rules WHERE id = $1", createdRuleID)
		}
		if createdROIID != "" {
			_, _ = store.Pool().Exec(ctx, "DELETE FROM camera_rois WHERE id = $1", createdROIID)
		}
		if createdCamID != "" {
			_, _ = store.Pool().Exec(ctx, "DELETE FROM cameras WHERE id = $1", createdCamID)
		}
		if createdVerID != "" {
			_, _ = store.Pool().Exec(ctx, "DELETE FROM algorithm_versions WHERE id = $1", createdVerID)
		}
		if createdAlgoID != "" {
			_, _ = store.Pool().Exec(ctx, "DELETE FROM algorithms WHERE id = $1", createdAlgoID)
		}
	})

	// 1. Create algorithm & version
	algoKey := "crud_algo_" + uuid.Must(uuid.NewV7()).String()[24:]
	w := doJSONReq(t, r, http.MethodPost, "/api/v1/algorithms", adminTok, map[string]any{
		"algorithm_key":    algoKey,
		"name":             "Helmet Detect",
		"supported_labels": []string{"person", "no_helmet"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create algorithm status=%d body=%s", w.Code, w.Body.String())
	}
	var algoResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &algoResp)
	createdAlgoID = algoResp.Data.ID

	w = doJSONReq(t, r, http.MethodPost, "/api/v1/algorithms/"+createdAlgoID+"/versions", adminTok, map[string]any{
		"version":        "v1.0.0",
		"runtime_key":    "py-yolo-v1",
		"default_params": map[string]any{"target_fps": 5},
		"status":         "active",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create version status=%d body=%s", w.Code, w.Body.String())
	}
	var verResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &verResp)
	createdVerID = verResp.Data.ID

	// 2. Create camera
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/cameras", adminTok, map[string]any{
		"name":     "Press Line 1",
		"location": "Workshop A",
		"rtsp_url": "rtsp://192.168.0.201:554/stream1",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create camera status=%d body=%s", w.Code, w.Body.String())
	}
	var camResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &camResp)
	createdCamID = camResp.Data.ID

	// 3. Reject invalid ROI coordinates (> 1.0)
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/cameras/"+createdCamID+"/rois", adminTok, map[string]any{
		"name":     "Invalid ROI",
		"roi_type": "polygon",
		"points": []map[string]float64{
			{"x": 0.1, "y": 0.1},
			{"x": 1.5, "y": 0.1},
			{"x": 0.5, "y": 0.8},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid ROI status=%d, want 400 (body=%s)", w.Code, w.Body.String())
	}

	// 4. Create valid ROI
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/cameras/"+createdCamID+"/rois", adminTok, map[string]any{
		"name":     "Danger Zone",
		"roi_type": "polygon",
		"points": []map[string]float64{
			{"x": 0.1, "y": 0.2},
			{"x": 0.8, "y": 0.2},
			{"x": 0.8, "y": 0.9},
			{"x": 0.1, "y": 0.9},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create valid ROI status=%d body=%s", w.Code, w.Body.String())
	}
	var roiResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &roiResp)
	createdROIID = roiResp.Data.ID

	// 5. Bind algorithm rule to ROI
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/cameras/"+createdCamID+"/rules", adminTok, map[string]any{
		"roi_id":               createdROIID,
		"algorithm_version_id": createdVerID,
		"name":                 "No Helmet Rule",
		"rule_mode":            "anomaly_detect",
		"params": map[string]any{
			"labels":             []string{"no_helmet"},
			"min_confidence":     0.6,
			"consecutive_frames": 3,
			"cooldown_seconds":   30,
		},
		"record_video": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create rule status=%d body=%s", w.Code, w.Body.String())
	}
	var ruleResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ruleResp)
	createdRuleID = ruleResp.Data.ID

	// 6. Request WebRTC stream token
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/cameras/"+createdCamID+"/stream-token", adminTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stream-token status=%d body=%s", w.Code, w.Body.String())
	}

	pub.mu.Lock()
	eventCount := len(pub.configEvents)
	pub.mu.Unlock()
	if eventCount < 3 {
		t.Fatalf("expected >= 3 config events published on camera/roi/rule mutations, got %d", eventCount)
	}
}

func TestAlarmHandleAuditAndManualRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, store, pub, tm := setupAPITestRouter(t)

	ctx := context.Background()
	opID := uuid.Must(uuid.NewV7())
	camID := uuid.Must(uuid.NewV7())
	alarmID := uuid.Must(uuid.NewV7())
	chanID := uuid.Must(uuid.NewV7())
	deliveryID := uuid.Must(uuid.NewV7())

	t.Cleanup(func() {
		_, _ = store.Pool().Exec(ctx, "DELETE FROM alarm_deliveries WHERE id = $1", deliveryID)
		_, _ = store.Pool().Exec(ctx, "DELETE FROM alarms WHERE id = $1", alarmID)
		_, _ = store.Pool().Exec(ctx, "DELETE FROM alarm_channels WHERE id = $1", chanID)
		_, _ = store.Pool().Exec(ctx, "DELETE FROM cameras WHERE id = $1", camID)
		_, _ = store.Pool().Exec(ctx, "DELETE FROM users WHERE id = $1", opID)
	})

	pwHash, _ := auth.HashPassword("OpPass!2026")
	_, err := store.Queries().CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:           opID,
		Username:     "op_" + opID.String()[24:],
		PasswordHash: pwHash,
		DisplayName:  "Operator One",
		Role:         domain.RoleOperator,
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateUser(): %v", err)
	}

	_, err = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID:         camID,
		Name:       "Audit Cam",
		RtspUrl:    "rtsp://127.0.0.1:8554/audit",
		StreamPath: "cam_audit_" + camID.String()[24:],
		CodecHint:  "h264",
		Status:     "online",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateCamera(): %v", err)
	}

	_, err = store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
		ID:                 alarmID,
		CameraID:           camID,
		RuleMode:           domain.RuleModeAnomalyDetect,
		Title:              "Intrusion Detected",
		Severity:           domain.SeverityCritical,
		TriggeredAt:        time.Now().UTC(),
		DetectedCount:      1,
		DetectionsSnapshot: []byte(`[]`),
		RoiSnapshot:        []byte(`{}`),
		VideoStatus:        domain.VideoStatusNone,
	})
	if err != nil {
		t.Fatalf("CreateAlarm(): %v", err)
	}

	_, err = store.Queries().CreateAlarmChannel(ctx, sqlcgen.CreateAlarmChannelParams{
		ID:          chanID,
		Name:        "Factory Webhook",
		ChannelType: "webhook",
		Config:      []byte(`{"url":"http://127.0.0.1:9999/hook"}`),
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateAlarmChannel(): %v", err)
	}

	_, err = store.Queries().CreateAlarmDelivery(ctx, sqlcgen.CreateAlarmDeliveryParams{
		ID:          deliveryID,
		AlarmID:     alarmID,
		ChannelID:   chanID,
		ChannelType: "webhook",
		Status:      "failed",
		MaxAttempts: 4,
	})
	if err != nil {
		t.Fatalf("CreateAlarmDelivery(): %v", err)
	}

	opTok, _, _, _ := tm.IssueAccessToken(opID.String(), "op_user", domain.RoleOperator)
	_, viewerTok := seedTestUserToken(t, store, tm, domain.RoleViewer)

	// Viewer cannot handle alarm (403)
	w := doJSONReq(t, r, http.MethodPost, "/api/v1/alarms/"+alarmID.String()+"/handle", viewerTok, map[string]any{
		"handle_status": "confirmed",
		"handle_note":   "should be forbidden",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer handle alarm status=%d, want 403", w.Code)
	}

	// Operator handles alarm -> records handled_by & handled_at
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/alarms/"+alarmID.String()+"/handle", opTok, map[string]any{
		"handle_status": "confirmed",
		"handle_note":   "verified by operator",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("operator handle alarm status=%d body=%s", w.Code, w.Body.String())
	}

	updated, err := store.Queries().GetAlarmByID(ctx, alarmID)
	if err != nil {
		t.Fatalf("GetAlarmByID(): %v", err)
	}
	if updated.HandleStatus != domain.HandleStatusConfirmed || !updated.HandledBy.Valid || updated.HandledBy.UUID != opID || updated.HandledAt == nil {
		t.Fatalf("unexpected audited alarm record: %+v", updated)
	}

	// Operator retries failed delivery -> republishes to RabbitMQ
	w = doJSONReq(t, r, http.MethodPost, "/api/v1/deliveries/"+deliveryID.String()+"/retry", opTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("retry delivery status=%d body=%s", w.Code, w.Body.String())
	}

	pub.mu.Lock()
	alarmPubCount := len(pub.alarmEvents)
	pub.mu.Unlock()
	if alarmPubCount != 1 {
		t.Fatalf("expected 1 republished alarm delivery message, got %d", alarmPubCount)
	}
}
