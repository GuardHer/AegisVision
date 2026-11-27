package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// Review Focus #4: Audit logs redact passwords/secrets/tokens; settings & health work; algo versions include 30d FP stats.
func TestGovernanceAuditRedactionSettingsHealthAndAlgoStats(t *testing.T) {
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AEGIS_TEST_POSTGRES_DSN not set")
	}
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	defer store.Close()

	tokens := auth.NewTokenManager("test-gov-secret-32-chars!!!!!!!!", time.Hour)
	router := api.NewRouter(api.RouterDeps{
		Store:  store,
		Tokens: tokens,
	})

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	adminName := "gov_admin_" + suffix
	adminUser, err := store.Queries().CreateUser(ctx, sqlcgen.CreateUserParams{
		ID: uuid.Must(uuid.NewV7()), Username: adminName, PasswordHash: "hash",
		DisplayName: "Gov Admin", Role: domain.RoleAdmin, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	adminTok, _, _, _ := tokens.IssueAccessToken(adminUser.ID.String(), adminName, domain.RoleAdmin)
	viewerTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "gov_vw_"+suffix, domain.RoleViewer)

	// 1. Trigger audited user create, reset-password, and channel create containing sensitive values.
	secretPass := "SuperSecretPass!999"
	secretNewPass := "BrandNewSecretPass!888"
	secretChanKey := "SEC_dingtalk_top_secret_xyz"

	wUser := doAPIReq(t, router, http.MethodPost, "/api/v1/users", adminTok, map[string]any{
		"username":     "u_gov_" + suffix,
		"password":     secretPass,
		"display_name": "Gov Target",
		"role":         domain.RoleOperator,
	})
	if wUser.Code != http.StatusCreated {
		t.Fatalf("create user failed: status=%d body=%s", wUser.Code, wUser.Body.String())
	}
	var userEnv struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wUser.Body.Bytes(), &userEnv)

	wReset := doAPIReq(t, router, http.MethodPost, "/api/v1/users/"+userEnv.Data.ID.String()+"/reset-password", adminTok, map[string]any{
		"new_password": secretNewPass,
	})
	if wReset.Code != http.StatusOK {
		t.Fatalf("reset password failed: status=%d body=%s", wReset.Code, wReset.Body.String())
	}

	wChan := doAPIReq(t, router, http.MethodPost, "/api/v1/channels", adminTok, map[string]any{
		"name":         "GovDing-" + suffix,
		"channel_type": "dingtalk",
		"config":       map[string]any{"webhook_url": "https://oapi.dingtalk.com/robot/send?access_token=abc", "secret": secretChanKey},
	})
	if wChan.Code != http.StatusCreated {
		t.Fatalf("create channel failed: status=%d body=%s", wChan.Code, wChan.Body.String())
	}
	var chanEnv struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wChan.Body.Bytes(), &chanEnv)

	// Seed algorithm + version + 2 alarms (1 confirmed, 1 false_positive) for 30d stats.
	algoID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
		ID: algoID, AlgorithmKey: "gov_algo_" + suffix, Name: "GovAlgo-" + suffix, SupportedLabels: json.RawMessage(`["smoking"]`),
	})
	verID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{
		ID: verID, AlgorithmID: algoID, Version: "1.0.0", RuntimeKey: "mock", LaunchSpec: json.RawMessage(`{}`), DefaultParams: json.RawMessage(`{}`), Status: "active",
	})
	camID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camID, Name: "GovCam-" + suffix, RtspUrl: "rtsp://127.0.0.1/gov", StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""),
		CodecHint: "h264", Status: "online", Enabled: true, Tags: json.RawMessage(`[]`),
	})
	for _, st := range []string{domain.HandleStatusConfirmed, domain.HandleStatusFalsePositive} {
		aid := uuid.Must(uuid.NewV7())
		_, _ = store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
			ID: aid, CameraID: camID, AlgorithmVersionID: uuid.NullUUID{UUID: verID, Valid: true},
			RuleMode: domain.RuleModeAnomalyDetect, Title: "GovAlarm", Severity: domain.SeverityWarning,
			TriggeredAt: time.Now().UTC(), DetectedCount: 1, DetectionsSnapshot: json.RawMessage(`[]`), RoiSnapshot: json.RawMessage(`{}`), VideoStatus: domain.VideoStatusNone,
		})
		_, _ = store.Queries().HandleAlarm(ctx, sqlcgen.HandleAlarmParams{ID: aid, HandleStatus: st})
	}

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id = $1`, camID)
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM audit_logs WHERE actor_username = $1`, adminName)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteAlgorithm(cleanCtx, algoID)
		_ = store.Queries().DeleteAlarmChannel(cleanCtx, chanEnv.Data.ID)
		_ = store.Queries().DeleteUser(cleanCtx, userEnv.Data.ID)
		_ = store.Queries().DeleteUser(cleanCtx, adminUser.ID)
	})

	// 2. Verify audit-logs contain our actions and ZERO sensitive secrets!
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/audit-logs", viewerTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer audit-logs, got %d", w.Code)
	}
	wAudit := doAPIReq(t, router, http.MethodGet, "/api/v1/audit-logs?actor_username="+adminName, adminTok, nil)
	if wAudit.Code != http.StatusOK {
		t.Fatalf("GET audit-logs failed: status=%d body=%s", wAudit.Code, wAudit.Body.String())
	}
	auditBody := wAudit.Body.String()
	for _, action := range []string{"user.create", "user.reset_password", "channel.create"} {
		if !strings.Contains(auditBody, action) {
			t.Fatalf("expected audit action %q in response: %s", action, auditBody)
		}
	}
	for _, leaked := range []string{secretPass, secretNewPass, secretChanKey} {
		if strings.Contains(auditBody, leaked) {
			t.Fatalf("SENSITIVE SECRET LEAKED into audit_logs: %q", leaked)
		}
	}

	// 3. Verify GET/PUT /api/v1/settings
	if w := doAPIReq(t, router, http.MethodPut, "/api/v1/settings", viewerTok, map[string]any{"platform_title": "hack"}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer PUT settings, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPut, "/api/v1/settings", adminTok, map[string]any{
		"platform_title":      "AegisVision Factory EHS",
		"default_alarm_chime": false,
		"popup_min_severity":  "critical",
		"max_export_rows":     800,
		"default_timezone":    "Asia/Shanghai",
	}); w.Code != http.StatusOK {
		t.Fatalf("PUT settings failed: status=%d body=%s", w.Code, w.Body.String())
	}
	wGetSet := doAPIReq(t, router, http.MethodGet, "/api/v1/settings", viewerTok, nil)
	if wGetSet.Code != http.StatusOK || !strings.Contains(wGetSet.Body.String(), `"max_export_rows":800`) {
		t.Fatalf("GET settings mismatch: status=%d body=%s", wGetSet.Code, wGetSet.Body.String())
	}

	// 4. Verify GET /api/v1/system/health
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/system/health", viewerTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer system/health, got %d", w.Code)
	}
	wHealth := doAPIReq(t, router, http.MethodGet, "/api/v1/system/health", adminTok, nil)
	if wHealth.Code != http.StatusOK || !strings.Contains(wHealth.Body.String(), `"postgresql"`) || !strings.Contains(wHealth.Body.String(), `"healthy"`) {
		t.Fatalf("GET system/health unexpected: status=%d body=%s", wHealth.Code, wHealth.Body.String())
	}

	// 5. Verify GET /api/v1/algorithms/:id/versions includes 30d FP stats (2 total, 1 confirmed, 1 fp -> 50.0%)
	wVers := doAPIReq(t, router, http.MethodGet, "/api/v1/algorithms/"+algoID.String()+"/versions", viewerTok, nil)
	if wVers.Code != http.StatusOK {
		t.Fatalf("GET algorithm versions failed: status=%d body=%s", wVers.Code, wVers.Body.String())
	}
	var versEnv struct {
		Data []api.AlgorithmVersionWithStats `json:"data"`
	}
	if err := json.Unmarshal(wVers.Body.Bytes(), &versEnv); err != nil || len(versEnv.Data) != 1 {
		t.Fatalf("unmarshal versions: err=%v body=%s", err, wVers.Body.String())
	}
	st := versEnv.Data[0].Stats30d
	if st.TotalAlarms != 2 || st.ConfirmedCount != 1 || st.FalsePositiveCount != 1 || st.FalsePositiveRate != 50.0 {
		t.Fatalf("unexpected 30d algo stats: %+v", st)
	}
}
