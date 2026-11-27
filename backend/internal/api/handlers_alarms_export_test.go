package api_test

import (
	"archive/zip"
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

func TestAlarmBatchHandleAndStreamingCSVZIPExport(t *testing.T) {
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

	tokens := auth.NewTokenManager("test-export-secret-32-chars!!!!!", time.Hour)
	router := api.NewRouter(api.RouterDeps{
		Store:   store,
		Storage: localStore,
		Tokens:  tokens,
	})

	opUser, err := store.Queries().CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:           uuid.Must(uuid.NewV7()),
		Username:     "op_exp_" + uuid.Must(uuid.NewV7()).String()[24:],
		PasswordHash: "hash",
		DisplayName:  "Op",
		Role:         domain.RoleOperator,
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opTok, _, _, _ := tokens.IssueAccessToken(opUser.ID.String(), opUser.Username, opUser.Role)

	camID := uuid.Must(uuid.NewV7())
	_, err = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID:         camID,
		Name:       "Export-Cam",
		RtspUrl:    "rtsp://127.0.0.1/exp",
		StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""),
		CodecHint:  "h264",
		Status:     "online",
		Enabled:    true,
		Tags:       []byte(`[]`),
	})
	if err != nil {
		t.Fatalf("create camera: %v", err)
	}

	imgKey := "images/2026/11/27/test-export.jpg"
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	if err := localStore.PutObject(ctx, imgKey, bytes.NewReader(jpegBytes), int64(len(jpegBytes)), "image/jpeg"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	a1ID := uuid.Must(uuid.NewV7())
	a2ID := uuid.Must(uuid.NewV7())
	for idx, pair := range []struct {
		id  uuid.UUID
		key string
	}{{a1ID, imgKey}, {a2ID, ""}} {
		_, err = store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
			ID:                 pair.id,
			CameraID:           camID,
			RuleMode:           domain.RuleModeAnomalyDetect,
			Title:              "Smoking Alert " + string(rune('A'+idx)),
			Severity:           domain.SeverityCritical,
			TriggeredAt:        time.Now().UTC(),
			DetectedCount:      1,
			DetectionsSnapshot: []byte(`[]`),
			RoiSnapshot:        []byte(`{}`),
			ImageObjectKey:     pair.key,
			VideoStatus:        domain.VideoStatusNone,
		})
		if err != nil {
			t.Fatalf("create alarm %d: %v", idx, err)
		}
	}

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id = $1`, camID)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteUser(cleanCtx, opUser.ID)
	})

	// Review Focus #5: Invalid batch-handle requests must return 400
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/batch-handle", opTok, map[string]any{
		"alarm_ids":     []string{},
		"handle_status": domain.HandleStatusConfirmed,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty alarm_ids, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/batch-handle", opTok, map[string]any{
		"alarm_ids":     []string{a1ID.String()},
		"handle_status": "bogus_status",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid handle_status, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/batch-handle", opTok, map[string]any{
		"alarm_ids":             []string{a1ID.String()},
		"handle_status":         domain.HandleStatusFalsePositive,
		"false_positive_reason": "invalid_reason",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid false_positive_reason, got %d", w.Code)
	}

	// Valid batch handle with false_positive_reason (and formula-prefix note to verify CSV neutralization)
	wBatch := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/batch-handle", opTok, map[string]any{
		"alarm_ids":             []string{a1ID.String(), a2ID.String()},
		"handle_status":         domain.HandleStatusFalsePositive,
		"false_positive_reason": domain.FalsePositiveLighting,
		"handle_note":           "=CMD|sun glare",
	})
	if wBatch.Code != http.StatusOK {
		t.Fatalf("batch-handle failed: status=%d body=%s", wBatch.Code, wBatch.Body.String())
	}
	gotA1, err := store.Queries().GetAlarmByID(ctx, a1ID)
	if err != nil || gotA1.HandleStatus != domain.HandleStatusFalsePositive || gotA1.FalsePositiveReason != domain.FalsePositiveLighting {
		t.Fatalf("unexpected updated alarm: %+v err=%v", gotA1, err)
	}

	// Single handle with ignored status
	wIgn := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/"+a2ID.String()+"/handle", opTok, map[string]any{
		"handle_status": domain.HandleStatusIgnored,
		"handle_note":   "maintenance drill",
	})
	if wIgn.Code != http.StatusOK {
		t.Fatalf("single handle ignored failed: status=%d body=%s", wIgn.Code, wIgn.Body.String())
	}

	// Review Focus #4: Streaming CSV export (with limit > 500 clamped cleanly)
	wCSV := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/export", opTok, map[string]any{
		"format":    "csv",
		"camera_id": camID.String(),
		"limit":     9999,
	})
	if wCSV.Code != http.StatusOK || !strings.Contains(wCSV.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv export failed: status=%d ct=%q", wCSV.Code, wCSV.Header().Get("Content-Type"))
	}
	csvText := wCSV.Body.String()
	if !strings.Contains(csvText, a1ID.String()) || !strings.Contains(csvText, a2ID.String()) {
		t.Fatalf("csv export missing alarms: %s", csvText)
	}
	if !strings.Contains(csvText, "'=CMD|sun glare") {
		t.Fatalf("expected formula-prefixed CSV note to be neutralized, got: %s", csvText)
	}

	// Review Focus #4: Streaming ZIP export succeeds cleanly with 1 snapshot + 1 snapshot-less alarm
	wZIP := doAPIReq(t, router, http.MethodPost, "/api/v1/alarms/export", opTok, map[string]any{
		"format":    "zip",
		"camera_id": camID.String(),
	})
	if wZIP.Code != http.StatusOK || !strings.Contains(wZIP.Header().Get("Content-Type"), "application/zip") {
		t.Fatalf("zip export failed: status=%d ct=%q", wZIP.Code, wZIP.Header().Get("Content-Type"))
	}
	zr, err := zip.NewReader(bytes.NewReader(wZIP.Body.Bytes()), int64(wZIP.Body.Len()))
	if err != nil {
		t.Fatalf("parse exported zip: %v", err)
	}
	var hasMeta, hasImg bool
	for _, f := range zr.File {
		if f.Name == "metadata.json" {
			hasMeta = true
			rc, _ := f.Open()
			var items []map[string]any
			_ = json.NewDecoder(rc).Decode(&items)
			_ = rc.Close()
			if len(items) != 2 {
				t.Fatalf("expected 2 entries in metadata.json, got %d", len(items))
			}
		}
		if f.Name == "images/"+a1ID.String()+".jpg" {
			hasImg = true
		}
	}
	if !hasMeta || !hasImg {
		t.Fatalf("zip missing expected files: hasMeta=%v hasImg=%v", hasMeta, hasImg)
	}
}
