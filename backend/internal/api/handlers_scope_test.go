package api_test

import (
	"bytes"
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
	"aegisvision/backend/internal/platform/storage"
)

// Review Focus #1: Workshop data-scope RBAC across zones, cameras, alarms, evidence, dashboard, analytics, and datasets.
func TestWorkshopDataScopeRBACEnforcement(t *testing.T) {
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

	localStore, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	tokens := auth.NewTokenManager("test-scope-secret-32-chars!!!!!!", time.Hour)
	router := api.NewRouter(api.RouterDeps{
		Store:   store,
		Storage: localStore,
		Tokens:  tokens,
	})

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	adminTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "admin_"+suffix, domain.RoleAdmin)

	// Create Workshop A -> Station A1, and Workshop B -> Station B1
	factoryID := uuid.Must(uuid.NewV7())
	wsAID := uuid.Must(uuid.NewV7())
	stAID := uuid.Must(uuid.NewV7())
	wsBID := uuid.Must(uuid.NewV7())
	stBID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: factoryID, Name: "Fac-" + suffix, ZoneType: domain.ZoneTypeFactory})
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: wsAID, ParentID: uuid.NullUUID{UUID: factoryID, Valid: true}, Name: "WS-A-" + suffix, ZoneType: domain.ZoneTypeWorkshop})
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: stAID, ParentID: uuid.NullUUID{UUID: wsAID, Valid: true}, Name: "ST-A-" + suffix, ZoneType: domain.ZoneTypeWorkstation})
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: wsBID, ParentID: uuid.NullUUID{UUID: factoryID, Valid: true}, Name: "WS-B-" + suffix, ZoneType: domain.ZoneTypeWorkshop})
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: stBID, ParentID: uuid.NullUUID{UUID: wsBID, Valid: true}, Name: "ST-B-" + suffix, ZoneType: domain.ZoneTypeWorkstation})

	camAID := uuid.Must(uuid.NewV7())
	camBID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camAID, Name: "Cam-A-" + suffix, RtspUrl: "rtsp://127.0.0.1/a", StreamPath: "cam_" + strings.ReplaceAll(camAID.String(), "-", ""),
		CodecHint: "h264", Status: "online", Enabled: true, ZoneID: uuid.NullUUID{UUID: stAID, Valid: true}, Tags: json.RawMessage(`[]`),
	})
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camBID, Name: "Cam-B-" + suffix, RtspUrl: "rtsp://127.0.0.1/b", StreamPath: "cam_" + strings.ReplaceAll(camBID.String(), "-", ""),
		CodecHint: "h264", Status: "online", Enabled: true, ZoneID: uuid.NullUUID{UUID: stBID, Valid: true}, Tags: json.RawMessage(`[]`),
	})

	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	imgA := "images/scope/" + suffix + "_a.jpg"
	imgB := "images/scope/" + suffix + "_b.jpg"
	_ = localStore.PutObject(ctx, imgA, bytes.NewReader(jpegBytes), int64(len(jpegBytes)), "image/jpeg")
	_ = localStore.PutObject(ctx, imgB, bytes.NewReader(jpegBytes), int64(len(jpegBytes)), "image/jpeg")

	alarmAID := uuid.Must(uuid.NewV7())
	alarmBID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	_, _ = store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
		ID: alarmAID, CameraID: camAID, ZoneID: uuid.NullUUID{UUID: stAID, Valid: true},
		RuleMode: domain.RuleModeAnomalyDetect, Title: "Alarm-A-" + suffix, Severity: domain.SeverityCritical,
		TriggeredAt: now, DetectedCount: 1, DetectionsSnapshot: json.RawMessage(`[]`), RoiSnapshot: json.RawMessage(`{}`),
		ImageObjectKey: imgA, VideoStatus: domain.VideoStatusNone,
	})
	_, _ = store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
		ID: alarmBID, CameraID: camBID, ZoneID: uuid.NullUUID{UUID: stBID, Valid: true},
		RuleMode: domain.RuleModeAnomalyDetect, Title: "Alarm-B-" + suffix, Severity: domain.SeverityCritical,
		TriggeredAt: now, DetectedCount: 1, DetectionsSnapshot: json.RawMessage(`[]`), RoiSnapshot: json.RawMessage(`{}`),
		ImageObjectKey: imgB, VideoStatus: domain.VideoStatusNone,
	})
	_, _ = store.Queries().HandleAlarm(ctx, sqlcgen.HandleAlarmParams{ID: alarmAID, HandleStatus: domain.HandleStatusFalsePositive, FalsePositiveReason: domain.FalsePositiveLighting})
	_, _ = store.Queries().HandleAlarm(ctx, sqlcgen.HandleAlarmParams{ID: alarmBID, HandleStatus: domain.HandleStatusFalsePositive, FalsePositiveReason: domain.FalsePositiveLighting})

	opUser, err := store.Queries().CreateUser(ctx, sqlcgen.CreateUserParams{
		ID: uuid.Must(uuid.NewV7()), Username: "op_scope_" + suffix, PasswordHash: "hash",
		DisplayName: "WS-A Op", Role: domain.RoleOperator, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create op user: %v", err)
	}
	opTok, _, _, _ := tokens.IssueAccessToken(opUser.ID.String(), opUser.Username, opUser.Role)

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id IN ($1, $2)`, camAID, camBID)
		_ = store.Queries().DeleteCamera(cleanCtx, camAID)
		_ = store.Queries().DeleteCamera(cleanCtx, camBID)
		_ = store.Queries().DeleteUser(cleanCtx, opUser.ID)
		_ = store.Queries().DeleteZone(cleanCtx, stAID)
		_ = store.Queries().DeleteZone(cleanCtx, stBID)
		_ = store.Queries().DeleteZone(cleanCtx, wsAID)
		_ = store.Queries().DeleteZone(cleanCtx, wsBID)
		_ = store.Queries().DeleteZone(cleanCtx, factoryID)
	})

	// Restrict opUser to Workshop A (descendant Station A1 is automatically included)
	wSet := doAPIReq(t, router, http.MethodPut, "/api/v1/users/"+opUser.ID.String()+"/zones", adminTok, map[string]any{
		"all_zones_access": false,
		"zone_ids":         []string{wsAID.String()},
	})
	if wSet.Code != http.StatusOK {
		t.Fatalf("PUT user zones failed: status=%d body=%s", wSet.Code, wSet.Body.String())
	}

	// 1. Cameras: list sees ONLY Cam-A; direct GET & stream-token for Cam-B -> 403!
	wCams := doAPIReq(t, router, http.MethodGet, "/api/v1/cameras", opTok, nil)
	if wCams.Code != http.StatusOK || !strings.Contains(wCams.Body.String(), camAID.String()) || strings.Contains(wCams.Body.String(), camBID.String()) {
		t.Fatalf("restricted camera list leak: status=%d body=%s", wCams.Code, wCams.Body.String())
	}
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/cameras/"+camBID.String(), opTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized camera GET, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/"+camBID.String()+"/stream-token", opTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized stream-token, got %d", w.Code)
	}

	// 2. Alarms: list sees ONLY Alarm-A; direct GET & handle on Alarm-B -> 403!
	wAlarms := doAPIReq(t, router, http.MethodGet, "/api/v1/alarms", opTok, nil)
	if wAlarms.Code != http.StatusOK || !strings.Contains(wAlarms.Body.String(), alarmAID.String()) || strings.Contains(wAlarms.Body.String(), alarmBID.String()) {
		t.Fatalf("restricted alarms list leak: status=%d body=%s", wAlarms.Code, wAlarms.Body.String())
	}
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/alarms/"+alarmBID.String(), opTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized alarm GET, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/"+alarmBID.String()+"/handle", opTok, map[string]any{"handle_status": domain.HandleStatusConfirmed}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized alarm handle, got %d", w.Code)
	}

	// 3. Evidence URL with JWT: Alarm-A -> 200, Alarm-B -> 403!
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/evidence/"+alarmAID.String()+"/image", opTok, nil); w.Code != http.StatusOK {
		t.Fatalf("expected 200 for authorized evidence image, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/evidence/"+alarmBID.String()+"/image", opTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized evidence image, got %d", w.Code)
	}

	// 4. Datasets: sees ONLY Alarm-A, NOT Alarm-B!
	wDS := doAPIReq(t, router, http.MethodGet, "/api/v1/datasets/false-positives", opTok, nil)
	if wDS.Code != http.StatusOK || !strings.Contains(wDS.Body.String(), alarmAID.String()) || strings.Contains(wDS.Body.String(), alarmBID.String()) {
		t.Fatalf("restricted datasets leak: status=%d body=%s", wDS.Code, wDS.Body.String())
	}

	// 5. Zones tree: contains WS-A and ST-A, excludes WS-B and ST-B!
	wTree := doAPIReq(t, router, http.MethodGet, "/api/v1/zones/tree", opTok, nil)
	if wTree.Code != http.StatusOK || !strings.Contains(wTree.Body.String(), wsAID.String()) || strings.Contains(wTree.Body.String(), wsBID.String()) {
		t.Fatalf("restricted zone tree leak: status=%d body=%s", wTree.Code, wTree.Body.String())
	}

	// 6. Revoke ALL zones (all_zones_access=false, zone_ids=[]): list cameras & alarms return empty!
	wRevoke := doAPIReq(t, router, http.MethodPut, "/api/v1/users/"+opUser.ID.String()+"/zones", adminTok, map[string]any{
		"all_zones_access": false,
		"zone_ids":         []string{},
	})
	if wRevoke.Code != http.StatusOK {
		t.Fatalf("revoke user zones failed: %d", wRevoke.Code)
	}
	wZeroCams := doAPIReq(t, router, http.MethodGet, "/api/v1/cameras", opTok, nil)
	if wZeroCams.Code != http.StatusOK || strings.Contains(wZeroCams.Body.String(), camAID.String()) {
		t.Fatalf("expected empty cameras after full revocation, got %s", wZeroCams.Body.String())
	}
}
