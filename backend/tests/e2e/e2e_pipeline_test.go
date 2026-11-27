//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/agent"
	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/evidence"
	"aegisvision/backend/internal/ingest"
	"aegisvision/backend/internal/notifier"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/rabbitmq"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/platform/telemetry"
	"aegisvision/backend/internal/rules"
	"aegisvision/backend/internal/scheduler"
	"aegisvision/backend/internal/ws"
)

type fakeMP4ClipGenerator struct{}

func (f *fakeMP4ClipGenerator) GenerateClip(_ context.Context, _ evidence.ClipInput, outputPath string) error {
	// Produce a deterministic 2048-byte MP4-header-prefixed clip so S3 upload + HTTP 206 Range streaming
	// are verified end-to-end without requiring an external live RTSP feed or host ffmpeg binary.
	buf := make([]byte, 2048)
	copy(buf, []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p',
		'i', 's', 'o', 'm', 0x00, 0x00, 0x02, 0x00,
		'i', 's', 'o', 'm', 'm', 'p', '4', '1',
	})
	for i := 24; i < len(buf); i++ {
		buf[i] = byte(i % 251)
	}
	return os.WriteFile(outputPath, buf, 0o600)
}

func (f *fakeMP4ClipGenerator) GenerateSnapshot(_ context.Context, _, outputPath string) error {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	return os.WriteFile(outputPath, jpeg, 0o600)
}

type webhookDeliveryCapture struct {
	Event       string
	Signature   string
	Timestamp   string
	RawBody     []byte
	AlarmID     string
	RuleMode    string
	ImageURL    string
	VideoURL    string
}

type apiEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func TestE2EFullPipeline(t *testing.T) {
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("AEGIS_TEST_REDIS_ADDR")
	rmqURL := os.Getenv("AEGIS_TEST_RABBITMQ_URL")
	s3Endpoint := os.Getenv("AEGIS_TEST_S3_ENDPOINT")
	mtxAPIURL := os.Getenv("AEGIS_TEST_MEDIAMTX_API_URL")
	if dsn == "" || redisAddr == "" || rmqURL == "" || s3Endpoint == "" || mtxAPIURL == "" {
		t.Skip("AEGIS_TEST_* live infrastructure env vars not set; skipping full E2E pipeline test")
	}

	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	log := logger.New(logger.Options{Service: "aegis-e2e", Level: "warn", Format: "json"})

	// 1. Connect to live infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO S3, MediaMTX)
	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pgStore, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() { pgStore.Close() })

	redisClient, err := platformredis.New(ctx, redisAddr, os.Getenv("AEGIS_TEST_REDIS_PASSWORD"), 0)
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	rmqClient, err := rabbitmq.New(rmqURL)
	if err != nil {
		t.Fatalf("connect rabbitmq: %v", err)
	}
	t.Cleanup(func() { _ = rmqClient.Close() })
	if err := rmqClient.EnsureTopology(ctx); err != nil {
		t.Fatalf("ensure rabbitmq topology: %v", err)
	}

	s3Bucket := os.Getenv("AEGIS_TEST_S3_BUCKET")
	if s3Bucket == "" {
		s3Bucket = "aegis-evidence"
	}
	objStore, err := storage.New(ctx, config.StorageConfig{
		Driver: "s3",
		S3: config.S3Config{
			Endpoint:     s3Endpoint,
			Bucket:       s3Bucket,
			AccessKey:    os.Getenv("AEGIS_TEST_S3_ACCESS_KEY"),
			SecretKey:    os.Getenv("AEGIS_TEST_S3_SECRET_KEY"),
			Region:       "us-east-1",
			UsePathStyle: true,
		},
	})
	if err != nil {
		t.Fatalf("init s3 storage: %v", err)
	}

	mtxClient := mediamtx.New(mtxAPIURL, "rtsp://127.0.0.1:8554", "/whep", nil)

	jwtSecret := "e2e-jwt-secret-key-at-least-32-chars!"
	signSecret := "e2e-sign-secret-key-at-least-32-chars!"
	agentToken := "e2e-agent-token-" + suffix
	webhookSecret := "e2e-webhook-hmac-secret-" + suffix
	ingestInstanceID := "e2e-ingest-" + suffix
	nodeID := "e2e-node-" + suffix
	anomalyRuntimeKey := "py-e2e-anomaly-" + suffix
	absenceRuntimeKey := "py-e2e-absence-" + suffix
	sopRuntimeKey := "py-e2e-sop-" + suffix

	if _, err := api.BootstrapDefaultAdmin(ctx, pgStore, "admin", "Admin@123456"); err != nil {
		t.Fatalf("bootstrap default admin: %v", err)
	}

	tokenMgr := auth.NewTokenManager(jwtSecret, time.Hour)
	signer := auth.NewEvidenceSigner(signSecret)

	// Cleanup registry so live VM DB & S3 remain spotless after test completion.
	var (
		cleanupMu   sync.Mutex
		createdCams []string
		createdAlgo []string
		createdChs  []string
		s3Keys      []string
	)
	trackCam := func(id string) { cleanupMu.Lock(); createdCams = append(createdCams, id); cleanupMu.Unlock() }
	trackAlgo := func(id string) { cleanupMu.Lock(); createdAlgo = append(createdAlgo, id); cleanupMu.Unlock() }
	trackCh := func(id string) { cleanupMu.Lock(); createdChs = append(createdChs, id); cleanupMu.Unlock() }
	trackS3 := func(key string) {
		if key == "" {
			return
		}
		cleanupMu.Lock()
		s3Keys = append(s3Keys, key)
		cleanupMu.Unlock()
	}

	svcCtx, stopSvcs := context.WithCancel(context.Background())
	var svcWG sync.WaitGroup

	// 2. Start aegis-api HTTP server
	apiHealth := telemetry.NewHealth("aegis-api")
	apiHealth.SetReady(true)
	apiRouter := api.NewRouter(api.RouterDeps{
		Store:           pgStore,
		Cache:           redisClient,
		Publisher:       rmqClient,
		Storage:         objStore,
		MediaMTX:        mtxClient,
		Tokens:          tokenMgr,
		Signer:          signer,
		Health:          apiHealth,
		Logger:          log,
		RefreshTokenTTL: 24 * time.Hour,
	})
	apiSrv := httptest.NewServer(apiRouter)
	defer apiSrv.Close()

	// 3. Start aegis-ws server wired to live Redis Pub/Sub
	wsHub := ws.NewHub(ws.HubConfig{FrameBufferSize: 32, AlarmBufferSize: 64})
	wsSrv := ws.NewServer(ws.ServerDeps{
		Hub:       wsHub,
		Tokens:    tokenMgr,
		Blacklist: redisClient,
		Logger:    log,
	})
	wsHTTP := httptest.NewServer(wsSrv.Handler())
	defer wsHTTP.Close()

	framesCh, unsubFrames, err := redisClient.SubscribeFrames(svcCtx)
	if err != nil {
		t.Fatalf("subscribe redis frames: %v", err)
	}
	alarmsCh, unsubAlarms, err := redisClient.SubscribeRealtimeAlarms(svcCtx)
	if err != nil {
		t.Fatalf("subscribe redis alarms: %v", err)
	}
	svcWG.Add(2)
	go func() {
		defer svcWG.Done()
		for msg := range framesCh {
			wsHub.BroadcastFrame(msg.CameraID, msg.Payload)
		}
	}()
	go func() {
		defer svcWG.Done()
		for payload := range alarmsCh {
			wsHub.BroadcastAlarm(payload)
		}
	}()

	// 4. Start aegis-ingest gRPC server + rule engine + RabbitMQ consumers
	ingestRepo := ingest.NewPostgresRepo(pgStore)
	ruleEngine := rules.NewEngine()
	ingestProc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine:  ruleEngine,
		Repo:    ingestRepo,
		Redis:   redisClient,
		MQ:      rmqClient,
		Storage: objStore,
		Logger:  log,
	})
	ingestGW := ingest.NewGateway(ingest.GatewayDeps{
		InstanceID: ingestInstanceID,
		AgentToken: agentToken,
		Repo:       ingestRepo,
		Redis:      redisClient,
		Processor:  ingestProc,
		Logger:     log,
	})

	ingestLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ingest grpc: %v", err)
	}
	grpcSrv := grpc.NewServer(grpc.MaxRecvMsgSize(16 * 1024 * 1024))
	agentv1.RegisterAgentGatewayServer(grpcSrv, ingestGW)
	svcWG.Add(1)
	go func() {
		defer svcWG.Done()
		_ = grpcSrv.Serve(ingestLis)
	}()

	cmdQueue, err := rmqClient.DeclareExclusiveBinding(ctx, rabbitmq.ExchangeControl, rabbitmq.AgentCommandRoutingKey(ingestInstanceID))
	if err != nil {
		t.Fatalf("bind ingest cmd queue: %v", err)
	}
	ingestCfgQueue, err := rmqClient.DeclareExclusiveBinding(ctx, rabbitmq.ExchangeConfig, "")
	if err != nil {
		t.Fatalf("bind ingest config queue: %v", err)
	}
	svcWG.Add(2)
	go func() {
		defer svcWG.Done()
		_ = rmqClient.Consume(svcCtx, cmdQueue, 8, ingestGW.HandleRoutedCommand)
	}()
	go func() {
		defer svcWG.Done()
		_ = rmqClient.Consume(svcCtx, ingestCfgQueue, 1, func(c context.Context, _ []byte) error {
			_ = ingestRepo.ReloadRules(c, ruleEngine)
			return nil
		})
	}()

	// 5. Start aegis-scheduler reconciler
	schedCfgQueue, err := rmqClient.DeclareExclusiveBinding(ctx, rabbitmq.ExchangeConfig, "")
	if err != nil {
		t.Fatalf("bind scheduler config queue: %v", err)
	}
	reconciler := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:        "e2e-scheduler-" + suffix,
		State:             scheduler.NewPostgresStateStore(pgStore),
		Redis:             redisClient,
		Bus:               rmqClient,
		MediaMTX:          mtxClient,
		Logger:            log,
		LeaderTTL:         10 * time.Second,
		LeaderRenewEvery:  2 * time.Second,
		ReconcileInterval: 500 * time.Millisecond,
		LeaseTTL:          20 * time.Second,
		NodeTimeout:       15 * time.Second,
	})
	svcWG.Add(2)
	go func() {
		defer svcWG.Done()
		_ = reconciler.Run(svcCtx)
	}()
	go func() {
		defer svcWG.Done()
		_ = rmqClient.Consume(svcCtx, schedCfgQueue, 1, func(_ context.Context, _ []byte) error {
			reconciler.TriggerReconcile()
			return nil
		})
	}()

	// 6. Start aegis-dispatcher (Webhook notifier + async MP4 evidence recorder)
	registry := notifier.NewRegistry()
	registry.Register(notifier.NewWebhookNotifier(nil))
	dispatcher := notifier.NewDispatcher(notifier.DispatcherDeps{
		Registry:      registry,
		Store:         notifier.NewPostgresDeliveryStore(pgStore),
		RetryBus:      rmqClient,
		Signer:        signer,
		PublicBaseURL: apiSrv.URL,
		EvidenceTTL:   time.Hour,
		Logger:        log,
	})
	recorder := evidence.NewRecorder(evidence.RecorderDeps{
		Repo:        evidence.NewPostgresVideoRepo(pgStore),
		Storage:     objStore,
		Generator:   &fakeMP4ClipGenerator{},
		RTSPBaseURL: "rtsp://127.0.0.1:8554",
		Logger:      log,
	})
	svcWG.Add(2)
	go func() {
		defer svcWG.Done()
		_ = rmqClient.Consume(svcCtx, rabbitmq.QueueAlarmsTriggered, 4, dispatcher.HandleMessage)
	}()
	go func() {
		defer svcWG.Done()
		_ = rmqClient.Consume(svcCtx, rabbitmq.QueueEvidenceRecord, 2, func(c context.Context, body []byte) error {
			_ = recorder.HandleMessage(c, body)
			return nil
		})
	}()

	// 7. Start Mock Webhook receiver server
	var (
		whMu        sync.Mutex
		whDeliveries []webhookDeliveryCapture
	)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed struct {
			AlarmID  string `json:"alarm_id"`
			RuleMode string `json:"rule_mode"`
			Evidence struct {
				ImageURL string `json:"image_url"`
				VideoURL string `json:"video_url"`
			} `json:"evidence"`
		}
		_ = json.Unmarshal(raw, &parsed)
		whMu.Lock()
		whDeliveries = append(whDeliveries, webhookDeliveryCapture{
			Event:     r.Header.Get("X-Aegis-Event"),
			Signature: r.Header.Get("X-Aegis-Signature"),
			Timestamp: r.Header.Get("X-Aegis-Timestamp"),
			RawBody:   raw,
			AlarmID:   parsed.AlarmID,
			RuleMode:  parsed.RuleMode,
			ImageURL:  parsed.Evidence.ImageURL,
			VideoURL:  parsed.Evidence.VideoURL,
		})
		whMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	// 8. Start real aegis-agent backed by real Python mock_runtime subprocesses
	repoRoot := findRepoRoot(t)
	pySDKDir := filepath.Join(repoRoot, "runtime", "python-sdk")
	pythonBin := findPythonBinary()

	bufDB := filepath.Join(t.TempDir(), "e2e-agent-buffer.sqlite")
	localBuf, err := agent.NewLocalBuffer(bufDB)
	if err != nil {
		t.Fatalf("init agent local buffer: %v", err)
	}

	rtMgr := agent.NewRuntimeManager(agent.RuntimeManagerDeps{
		Specs: map[string]agent.RuntimeSpec{
			anomalyRuntimeKey: {
				RuntimeKey:      anomalyRuntimeKey,
				Command:         pythonBin,
				Args:            []string{"-m", "mock_runtime.main"},
				WorkDir:         pySDKDir,
				Env:             map[string]string{"PYTHONPATH": pySDKDir, "AEGIS_MOCK_MODE": "anomaly"},
				IdleGracePeriod: 2 * time.Second,
			},
			absenceRuntimeKey: {
				RuntimeKey:      absenceRuntimeKey,
				Command:         pythonBin,
				Args:            []string{"-m", "mock_runtime.main"},
				WorkDir:         pySDKDir,
				Env:             map[string]string{"PYTHONPATH": pySDKDir, "AEGIS_MOCK_MODE": "absence"},
				IdleGracePeriod: 2 * time.Second,
			},
			sopRuntimeKey: {
				RuntimeKey:      sopRuntimeKey,
				Command:         pythonBin,
				Args:            []string{"-m", "mock_runtime.main"},
				WorkDir:         pySDKDir,
				Env:             map[string]string{"PYTHONPATH": pySDKDir, "AEGIS_MOCK_MODE": "sop", "AEGIS_SOP_SCRIPT": "out_of_order"},
				IdleGracePeriod: 2 * time.Second,
			},
		},
		Logger: log,
	})
	nodeAgent := agent.NewNodeAgent(agent.NodeAgentDeps{
		NodeID:            nodeID,
		Hostname:          "e2e-host",
		IPAddr:            "127.0.0.1",
		IngestAddr:        ingestLis.Addr().String(),
		AgentToken:        agentToken,
		MaxTasks:          8,
		HeartbeatInterval: 2 * time.Second,
		RuntimeMgr:        rtMgr,
		Buffer:            localBuf,
		Logger:            log,
	})
	svcWG.Add(1)
	go func() {
		defer svcWG.Done()
		_ = nodeAgent.Run(svcCtx)
	}()

	// Register deterministic teardown BEFORE any test mutations so cleanup ALWAYS runs.
	t.Cleanup(func() {
		stopSvcs()
		grpcSrv.Stop()
		_ = unsubFrames()
		_ = unsubAlarms()
		rtMgr.Shutdown()
		_ = localBuf.Close()
		svcWG.Wait()

		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanCancel()

		cleanupMu.Lock()
		cams := append([]string(nil), createdCams...)
		algos := append([]string(nil), createdAlgo...)
		chs := append([]string(nil), createdChs...)
		keys := append([]string(nil), s3Keys...)
		cleanupMu.Unlock()

		for _, key := range keys {
			_ = objStore.DeleteObject(cleanCtx, key)
		}
		for _, camID := range cams {
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id = $1`, camID)
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM inference_tasks WHERE camera_id = $1`, camID)
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM cameras WHERE id = $1`, camID)
		}
		for _, algoID := range algos {
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM algorithm_versions WHERE algorithm_id = $1`, algoID)
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM algorithms WHERE id = $1`, algoID)
		}
		for _, chID := range chs {
			_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM alarm_channels WHERE id = $1`, chID)
		}
		_, _ = pgStore.Pool().Exec(cleanCtx, `DELETE FROM agent_nodes WHERE node_id = $1`, nodeID)
	})

	// Wait until aegis-agent has registered in PostgreSQL as online.
	waitUntil(t, 10*time.Second, "agent node online in postgres", func() bool {
		var status string
		err := pgStore.Pool().QueryRow(ctx, `SELECT status FROM agent_nodes WHERE node_id = $1`, nodeID).Scan(&status)
		return err == nil && status == "online"
	})

	// =========================================================================
	// ASSERTION 1: Admin Login, Resource CRUD, Stream Token & MediaMTX Auth
	// =========================================================================
	jwtToken := loginAdmin(t, apiSrv.URL, "admin", "Admin@123456")

	// Create Webhook channel
	chResp := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/channels", jwtToken, map[string]any{
		"name":         "e2e-webhook-" + suffix,
		"channel_type": "webhook",
		"config": map[string]any{
			"url":                   webhookSrv.URL,
			"method":                "POST",
			"secret":                webhookSecret,
			"timeout_ms":            5000,
			"include_evidence_urls": true,
		},
	})
	trackCh(chResp.ID)

	// Create Algorithm & two Versions (anomaly + absence)
	algoResp := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/algorithms", jwtToken, map[string]any{
		"algorithm_key":    "e2e_algo_" + suffix,
		"name":             "E2E Mock Detector",
		"supported_labels": []string{"no_helmet", "person"},
	})
	trackAlgo(algoResp.ID)

	anomalyVer := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/algorithms/"+algoResp.ID+"/versions", jwtToken, map[string]any{
		"version":        "1.0.0",
		"runtime_key":    anomalyRuntimeKey,
		"default_params": map[string]any{"mock_mode": "anomaly"},
		"status":         "active",
	})
	absenceVer := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/algorithms/"+algoResp.ID+"/versions", jwtToken, map[string]any{
		"version":        "1.1.0",
		"runtime_key":    absenceRuntimeKey,
		"default_params": map[string]any{"mock_mode": "absence"},
		"status":         "active",
	})

	// Create Camera #1 (Anomaly + Video Evidence) and full-frame ROI
	cam1 := doJSON[struct {
		ID         string `json:"id"`
		StreamPath string `json:"stream_path"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras", jwtToken, map[string]any{
		"name":     "E2E-Cam-Anomaly-" + suffix,
		"location": "Workshop-A",
		"rtsp_url": "rtsp://127.0.0.1:8554/mock-source-1",
	})
	trackCam(cam1.ID)

	roi1 := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+cam1.ID+"/rois", jwtToken, map[string]any{
		"name":     "Zone-A",
		"roi_type": domain.ROITypePolygon,
		"points": []map[string]float64{
			{"x": 0.05, "y": 0.05},
			{"x": 0.95, "y": 0.05},
			{"x": 0.95, "y": 0.95},
			{"x": 0.05, "y": 0.95},
		},
	})

	// Issue WebRTC play token and verify /internal/mediamtx/auth callback
	streamTok := doJSON[struct {
		Token      string `json:"token"`
		StreamPath string `json:"stream_path"`
		WHEPURL    string `json:"whep_url"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+cam1.ID+"/stream-token", jwtToken, nil)
	if streamTok.Token == "" || streamTok.StreamPath != cam1.StreamPath {
		t.Fatalf("unexpected stream-token response: %+v", streamTok)
	}

	assertMediaMTXAuth(t, apiSrv.URL, map[string]any{
		"action":   "read",
		"path":     cam1.StreamPath,
		"protocol": "webrtc",
		"query":    "token=" + streamTok.Token,
	}, http.StatusOK)
	assertMediaMTXAuth(t, apiSrv.URL, map[string]any{
		"action":   "read",
		"path":     cam1.StreamPath,
		"protocol": "webrtc",
		"query":    "token=invalid-token",
	}, http.StatusUnauthorized)
	assertMediaMTXAuth(t, apiSrv.URL, map[string]any{
		"action":   "read",
		"path":     "wrong_camera_path",
		"protocol": "webrtc",
		"query":    "token=" + streamTok.Token,
	}, http.StatusUnauthorized)

	// =========================================================================
	// ASSERTION 2: Anomaly Hit -> WS Live Boxes + Alarm Alert + Postgres + MinIO + Webhook
	// =========================================================================
	wsURL := "ws" + strings.TrimPrefix(wsHTTP.URL, "http") + "/ws/realtime?token=" + jwtToken
	wsConn, wsResp, err := websocket.Dial(ctx, wsURL, nil)
	if wsResp != nil && wsResp.Body != nil {
		_ = wsResp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial aegis-ws: %v", err)
	}
	defer func() { _ = wsConn.Close(websocket.StatusNormalClosure, "") }()

	subPayload, _ := json.Marshal(map[string]any{
		"type":       "subscribe",
		"camera_ids": []string{cam1.ID},
	})
	if err := wsConn.Write(ctx, websocket.MessageText, subPayload); err != nil {
		t.Fatalf("send ws subscribe: %v", err)
	}

	var (
		wsMu          sync.Mutex
		gotWSFrame    bool
		gotWSAlarm    bool
		wsAlarmID     string
	)
	wsReadCtx, cancelWSRead := context.WithCancel(ctx)
	defer cancelWSRead()
	go func() {
		for {
			_, data, readErr := wsConn.Read(wsReadCtx)
			if readErr != nil {
				return
			}
			var hdr struct {
				Type     string `json:"type"`
				CameraID string `json:"camera_id"`
				AlarmID  string `json:"alarm_id"`
				RuleMode string `json:"rule_mode"`
			}
			if json.Unmarshal(data, &hdr) != nil {
				continue
			}
			wsMu.Lock()
			if hdr.Type == "frame_detections" && hdr.CameraID == cam1.ID {
				gotWSFrame = true
			}
			if hdr.Type == "alarm_alert" && hdr.CameraID == cam1.ID && hdr.RuleMode == domain.RuleModeAnomalyDetect {
				gotWSAlarm = true
				if wsAlarmID == "" {
					wsAlarmID = hdr.AlarmID
				}
			}
			wsMu.Unlock()
		}
	}()

	// Create anomaly_detect rule (record_video=true) -> triggers scheduler -> agent -> Python mock_runtime
	_ = doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+cam1.ID+"/rules", jwtToken, map[string]any{
		"roi_id":               roi1.ID,
		"algorithm_version_id": anomalyVer.ID,
		"name":                 "No Helmet Detection",
		"rule_mode":            domain.RuleModeAnomalyDetect,
		"params": map[string]any{
			"labels":              []string{"no_helmet"},
			"require_is_abnormal": true,
			"min_confidence":      0.5,
			"hit_point":           "bottom_center",
			"consecutive_frames":  1,
			"duration_seconds":    0,
			"cooldown_seconds":    60,
			"severity":            "critical",
		},
		"record_video":       true,
		"video_pre_seconds":  5,
		"video_post_seconds": 5,
		"channel_ids":        []string{chResp.ID},
	})

	// Wait for WS frame_detections and alarm_alert
	waitUntil(t, 25*time.Second, "WS frame_detections and alarm_alert received", func() bool {
		wsMu.Lock()
		defer wsMu.Unlock()
		return gotWSFrame && gotWSAlarm
	})

	// Verify alarm persisted in Postgres via REST API
	var anomalyAlarmID string
	var anomalyImageKey string
	waitUntil(t, 10*time.Second, "anomaly alarm persisted in postgres", func() bool {
		list := doJSON[struct {
			Items []struct {
				ID             string `json:"id"`
				RuleMode       string `json:"rule_mode"`
				ImageObjectKey string `json:"image_object_key"`
			} `json:"items"`
		}](t, http.MethodGet, apiSrv.URL+"/api/v1/alarms?camera_id="+cam1.ID, jwtToken, nil)
		for _, item := range list.Items {
			if item.RuleMode == domain.RuleModeAnomalyDetect && item.ImageObjectKey != "" {
				anomalyAlarmID = item.ID
				anomalyImageKey = item.ImageObjectKey
				return true
			}
		}
		return false
	})
	trackS3(anomalyImageKey)

	// Verify JPEG snapshot is readable via JWT-authenticated evidence endpoint
	imgReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiSrv.URL+"/api/v1/evidence/"+anomalyAlarmID+"/image", nil)
	imgReq.Header.Set("Authorization", "Bearer "+jwtToken)
	imgResp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		t.Fatalf("fetch snapshot image: %v", err)
	}
	imgBytes, _ := io.ReadAll(imgResp.Body)
	_ = imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for snapshot image, got %d", imgResp.StatusCode)
	}
	if len(imgBytes) < 2 || imgBytes[0] != 0xFF || imgBytes[1] != 0xD8 {
		t.Fatalf("snapshot response is not valid JPEG (len=%d)", len(imgBytes))
	}

	// Wait for Mock Webhook delivery and verify HMAC-SHA256 signature + signed image URL
	var capturedWH webhookDeliveryCapture
	waitUntil(t, 15*time.Second, "webhook received alarm.triggered for anomaly alarm", func() bool {
		whMu.Lock()
		defer whMu.Unlock()
		for _, d := range whDeliveries {
			if d.AlarmID == anomalyAlarmID {
				capturedWH = d
				return true
			}
		}
		return false
	})
	if capturedWH.Event != "alarm.triggered" {
		t.Fatalf("unexpected webhook event header: %q", capturedWH.Event)
	}
	expectedSig := computeWebhookHMAC(webhookSecret, capturedWH.Timestamp, capturedWH.RawBody)
	if capturedWH.Signature != expectedSig {
		t.Fatalf("webhook HMAC mismatch: got %q, want %q", capturedWH.Signature, expectedSig)
	}
	if capturedWH.ImageURL == "" || !strings.Contains(capturedWH.ImageURL, "sig=") {
		t.Fatalf("webhook missing signed image_url: %q", capturedWH.ImageURL)
	}

	// Fetch signed image URL WITHOUT any JWT header
	signedImgResp, err := http.Get(capturedWH.ImageURL) // #nosec G107 -- local httptest server URL
	if err != nil {
		t.Fatalf("fetch signed image_url: %v", err)
	}
	signedImgBytes, _ := io.ReadAll(signedImgResp.Body)
	_ = signedImgResp.Body.Close()
	if signedImgResp.StatusCode != http.StatusOK || len(signedImgBytes) < 2 || signedImgBytes[0] != 0xFF {
		t.Fatalf("signed image_url failed: status=%d len=%d", signedImgResp.StatusCode, len(signedImgBytes))
	}

	// =========================================================================
	// ASSERTION 4 (check video backfill while anomaly alarm's async job runs):
	// Async MP4 uploaded to MinIO & backfilled -> signed Range bytes=0-63 -> 206
	// =========================================================================
	var videoKey string
	waitUntil(t, 15*time.Second, "async MP4 video backfilled with video_status=ready", func() bool {
		detail := doJSON[struct {
			VideoStatus    string `json:"video_status"`
			VideoObjectKey string `json:"video_object_key"`
		}](t, http.MethodGet, apiSrv.URL+"/api/v1/alarms/"+anomalyAlarmID, jwtToken, nil)
		if detail.VideoStatus == domain.VideoStatusReady && detail.VideoObjectKey != "" {
			videoKey = detail.VideoObjectKey
			return true
		}
		return false
	})
	trackS3(videoKey)

	if capturedWH.VideoURL == "" || !strings.Contains(capturedWH.VideoURL, "sig=") {
		t.Fatalf("webhook missing signed video_url: %q", capturedWH.VideoURL)
	}
	rangeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, capturedWH.VideoURL, nil)
	if err != nil {
		t.Fatalf("build range request: %v", err)
	}
	rangeReq.Header.Set("Range", "bytes=0-63")
	rangeResp, err := http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatalf("execute signed MP4 range request: %v", err)
	}
	rangeBody, _ := io.ReadAll(rangeResp.Body)
	_ = rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206 Partial Content for signed MP4 range, got %d", rangeResp.StatusCode)
	}
	if !strings.HasPrefix(rangeResp.Header.Get("Content-Range"), "bytes 0-63/") {
		t.Fatalf("unexpected Content-Range header: %q", rangeResp.Header.Get("Content-Range"))
	}
	if len(rangeBody) != 64 || string(rangeBody[4:8]) != "ftyp" {
		t.Fatalf("unexpected MP4 partial payload: len=%d header=%q", len(rangeBody), string(rangeBody[4:8]))
	}

	// =========================================================================
	// ASSERTION 3: Absence Detection via Empty Heartbeats + Timeout Pause Guard
	// =========================================================================
	camAbs := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras", jwtToken, map[string]any{
		"name":     "E2E-Cam-Absence-" + suffix,
		"location": "ControlRoom-B",
		"rtsp_url": "rtsp://127.0.0.1:8554/mock-source-2",
	})
	trackCam(camAbs.ID)

	roiAbs := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camAbs.ID+"/rois", jwtToken, map[string]any{
		"name":     "Operator-Desk",
		"roi_type": domain.ROITypeFullFrame,
		"points":   []map[string]float64{},
	})

	_ = doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camAbs.ID+"/rules", jwtToken, map[string]any{
		"roi_id":               roiAbs.ID,
		"algorithm_version_id": absenceVer.ID,
		"name":                 "Post Absence Guard",
		"rule_mode":            domain.RuleModeAbsenceDetect,
		"params": map[string]any{
			"labels":                    []string{"person"},
			"min_count":                 1,
			"duration_seconds":          2,
			"heartbeat_timeout_seconds": 4,
			"cooldown_seconds":          60,
			"severity":                  "warning",
		},
		"record_video": false,
	})

	// Real Python mock_runtime (AEGIS_MOCK_MODE=absence) emits 1Hz empty heartbeats -> triggers absence alarm.
	waitUntil(t, 25*time.Second, "absence_detect alarm triggered via live empty heartbeats", func() bool {
		list := doJSON[struct {
			Items []struct {
				ID       string `json:"id"`
				RuleMode string `json:"rule_mode"`
			} `json:"items"`
		}](t, http.MethodGet, apiSrv.URL+"/api/v1/alarms?camera_id="+camAbs.ID, jwtToken, nil)
		for _, item := range list.Items {
			if item.RuleMode == domain.RuleModeAbsenceDetect {
				return true
			}
		}
		return false
	})

	// Anti-false-alarm check: when heartbeat gap exceeds heartbeat_timeout_seconds, timer pauses/resets -> 0 alarms.
	camPause := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras", jwtToken, map[string]any{
		"name":     "E2E-Cam-Absence-Pause-" + suffix,
		"location": "ControlRoom-C",
		"rtsp_url": "rtsp://127.0.0.1:8554/mock-source-3",
	})
	trackCam(camPause.ID)

	roiPause := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camPause.ID+"/rois", jwtToken, map[string]any{
		"name":     "Desk-Pause",
		"roi_type": domain.ROITypeFullFrame,
		"points":   []map[string]float64{},
	})

	// Bind to a dummy version without running runtime so we control exact timestamps deterministically.
	pauseVer := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/algorithms/"+algoResp.ID+"/versions", jwtToken, map[string]any{
		"version":     "1.2.0",
		"runtime_key": "py-e2e-unassigned-" + suffix,
		"status":      "active",
	})
	_ = doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camPause.ID+"/rules", jwtToken, map[string]any{
		"roi_id":               roiPause.ID,
		"algorithm_version_id": pauseVer.ID,
		"name":                 "Pause On Disconnect",
		"rule_mode":            domain.RuleModeAbsenceDetect,
		"params": map[string]any{
			"labels":                    []string{"person"},
			"min_count":                 1,
			"duration_seconds":          3,
			"heartbeat_timeout_seconds": 2,
			"cooldown_seconds":          60,
			"severity":                  "warning",
		},
	})
	if err := ingestRepo.ReloadRules(ctx, ruleEngine); err != nil {
		t.Fatalf("reload rules for pause test: %v", err)
	}

	baseTS := time.Now().UTC()
	// Empty frame at T+0, then T+1s (elapsed=1s < 3s), then 5s stream outage gap (T+6s > timeout 2s => reset),
	// then T+7s (elapsed=1s < 3s). Total wall clock span is 7s > duration 3s, yet MUST produce 0 alarms!
	for _, offset := range []time.Duration{0, 1 * time.Second, 6 * time.Second, 7 * time.Second} {
		if err := ingestProc.ProcessLiveFrame(ctx, &agentv1.DetectionFrame{
			CameraId:           camPause.ID,
			AlgorithmVersionId: pauseVer.ID,
			TimestampMs:        baseTS.Add(offset).UnixMilli(),
			IsHeartbeat:        true,
		}); err != nil {
			t.Fatalf("ProcessLiveFrame pause check: %v", err)
		}
	}
	pauseAlarms := doJSON[struct {
		Total int64 `json:"total"`
	}](t, http.MethodGet, apiSrv.URL+"/api/v1/alarms?camera_id="+camPause.ID, jwtToken, nil)
	if pauseAlarms.Total != 0 {
		t.Fatalf("expected 0 false alarms when heartbeat gap exceeds timeout, got %d", pauseAlarms.Total)
	}

	// 12. Enterprise M2 E2E: Arming Schedule + Real Python Mock SOP Runtime -> sop_trace + sop_cycles + async snapshot fallback + Analytics!
	schedResp := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/schedules", jwtToken, map[string]any{
		"name":     "E2E-24x7-" + suffix,
		"timezone": "Asia/Shanghai",
		"weekly_windows": []map[string]any{
			{"days": []int{1, 2, 3, 4, 5, 6, 7}, "start": "00:00", "end": "24:00"},
		},
	})
	t.Cleanup(func() {
		if sid, pErr := uuid.Parse(schedResp.ID); pErr == nil {
			_ = pgStore.Queries().DeleteArmingSchedule(context.Background(), sid)
		}
	})

	sopVer := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/algorithms/"+algoResp.ID+"/versions", jwtToken, map[string]any{
		"version":     "2.0.0",
		"runtime_key": sopRuntimeKey,
		"status":      "active",
	})

	camSOP := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras", jwtToken, map[string]any{
		"name":             "E2E-Cam-SOP-" + suffix,
		"location":         "Assembly-Line-1",
		"workstation_code": "WS-SOP-01",
		"rtsp_url":         "rtsp://127.0.0.1:8554/mock-source-sop",
	})
	trackCam(camSOP.ID)

	roiSOP := doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camSOP.ID+"/rois", jwtToken, map[string]any{
		"name":     "ESD-Bench",
		"roi_type": domain.ROITypeFullFrame,
		"points":   []map[string]float64{},
	})

	_ = doJSON[struct {
		ID string `json:"id"`
	}](t, http.MethodPost, apiSrv.URL+"/api/v1/cameras/"+camSOP.ID+"/rules", jwtToken, map[string]any{
		"roi_id":               roiSOP.ID,
		"algorithm_version_id": sopVer.ID,
		"name":                 "ESD-Before-Scan",
		"rule_mode":            domain.RuleModeSOPSequence,
		"schedule_id":          schedResp.ID,
		"params": map[string]any{
			"trigger_mode":              "first_step",
			"heartbeat_timeout_seconds": 5,
			"cooldown_seconds":          60,
			"severity":                  "critical",
			"violation_types":           []string{"missing_step", "out_of_order", "step_timeout"},
			"steps": []map[string]any{
				{"step_id": "s1", "name": "Wear ESD Strap", "labels": []string{"wear_esd_strap"}, "min_confidence": 0.6, "required": true},
				{"step_id": "s2", "name": "Scan Barcode", "labels": []string{"scan_barcode"}, "min_confidence": 0.6, "required": true},
			},
		},
		"record_video": false,
	})

	var sopAlarmImgKey string
	waitUntil(t, 25*time.Second, "sop_sequence violation alarm with sop_trace and async snapshot fallback", func() bool {
		list := doJSON[struct {
			Items []struct {
				ID             string          `json:"id"`
				RuleMode       string          `json:"rule_mode"`
				ImageObjectKey string          `json:"image_object_key"`
				SOPTrace       json.RawMessage `json:"sop_trace"`
			} `json:"items"`
		}](t, http.MethodGet, apiSrv.URL+"/api/v1/alarms?camera_id="+camSOP.ID, jwtToken, nil)
		for _, item := range list.Items {
			if item.RuleMode == domain.RuleModeSOPSequence && len(item.SOPTrace) > 2 && item.ImageObjectKey != "" {
				sopAlarmImgKey = item.ImageObjectKey
				return true
			}
		}
		return false
	})
	trackS3(sopAlarmImgKey)

	// Verify analytics summary reflects the SOP violation step
	anSum := doJSON[api.AnalyticsSummaryPayload](t, http.MethodGet, apiSrv.URL+"/api/v1/analytics/summary?tz=Asia/Shanghai", jwtToken, nil)
	if anSum.KPIs.SOPViolatedCycles < 1 {
		t.Fatalf("expected analytics sop_violated_cycles >= 1, got %+v", anSum.KPIs)
	}
}

func loginAdmin(t *testing.T, baseURL, username, password string) string {
	t.Helper()
	resp := doJSON[struct {
		AccessToken string `json:"access_token"`
	}](t, http.MethodPost, baseURL+"/api/v1/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	if resp.AccessToken == "" {
		t.Fatal("login returned empty access_token")
	}
	return resp.AccessToken
}

func doJSON[T any](t *testing.T, method, url, token string, body any) T {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("create request %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute %s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s returned HTTP %d: %s", method, url, resp.StatusCode, string(rawResp))
	}

	var env apiEnvelope[T]
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("unmarshal response from %s %s: %v (body=%s)", method, url, err, string(rawResp))
	}
	if env.Code != 0 {
		t.Fatalf("%s %s returned error code %d: %s", method, url, env.Code, env.Message)
	}
	return env.Data
}

func assertMediaMTXAuth(t *testing.T, baseURL string, payload map[string]any, wantStatus int) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	resp, err := http.Post(baseURL+"/internal/mediamtx/auth", "application/json", bytes.NewReader(raw)) // #nosec G107 -- local test server
	if err != nil {
		t.Fatalf("call /internal/mediamtx/auth: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("/internal/mediamtx/auth returned %d, want %d (payload=%v)", resp.StatusCode, wantStatus, payload)
	}
}

func computeWebhookHMAC(secret, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(rawBody)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, desc)
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// thisFile = <repo>/backend/tests/e2e/e2e_pipeline_test.go -> go up 4 dirs
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func findPythonBinary() string {
	if p := os.Getenv("AEGIS_TEST_PYTHON"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

var _ = errors.Is
var _ = fmt.Sprintf
