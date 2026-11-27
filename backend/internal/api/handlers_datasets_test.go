package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestFalsePositiveDatasetsListAndZIPExport(t *testing.T) {
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
	tokens := auth.NewTokenManager("test-dataset-secret-32-chars!!!!", time.Hour)
	router := api.NewRouter(api.RouterDeps{
		Store:   store,
		Storage: localStore,
		Tokens:  tokens,
	})

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	opTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "op_"+suffix, domain.RoleOperator)
	viewerTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "vw_"+suffix, domain.RoleViewer)

	// Viewer MUST get 403 on dataset endpoints.
	if w := doAPIReq(t, router, http.MethodGet, "/api/v1/datasets/false-positives", viewerTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer list datasets, got %d", w.Code)
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/datasets/export", viewerTok, map[string]any{}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer export datasets, got %d", w.Code)
	}

	zoneID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{ID: zoneID, Name: "DS-Workshop-" + suffix, ZoneType: domain.ZoneTypeWorkshop})
	algoID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{ID: algoID, AlgorithmKey: "ds_algo_" + suffix, Name: "SmokeAlgo-" + suffix, SupportedLabels: json.RawMessage(`["smoking"]`)})
	verID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{ID: verID, AlgorithmID: algoID, Version: "3.0.0", RuntimeKey: "mock", LaunchSpec: json.RawMessage(`{}`), DefaultParams: json.RawMessage(`{}`), Status: "active"})
	camID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camID, Name: "DS-Cam-" + suffix, RtspUrl: "rtsp://127.0.0.1/ds", StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""),
		CodecHint: "h264", Status: "online", Enabled: true, ZoneID: uuid.NullUUID{UUID: zoneID, Valid: true}, Tags: json.RawMessage(`[]`),
	})

	imgKey := "images/ds/" + suffix + ".jpg"
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	if err := localStore.PutObject(ctx, imgKey, bytes.NewReader(jpegBytes), int64(len(jpegBytes)), "image/jpeg"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	fpWithImg := uuid.Must(uuid.NewV7())
	fpNoImg := uuid.Must(uuid.NewV7())
	confirmedWithImg := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	for _, spec := range []struct {
		id     uuid.UUID
		img    string
		status string
		reason string
		note   string
	}{
		{fpWithImg, imgKey, domain.HandleStatusFalsePositive, domain.FalsePositiveLighting, "sun glare"},
		{fpNoImg, "", domain.HandleStatusFalsePositive, domain.FalsePositiveLighting, "no snapshot"},
		{confirmedWithImg, imgKey, domain.HandleStatusConfirmed, "", "real incident"},
	} {
		_, cErr := store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
			ID: spec.id, CameraID: camID, ZoneID: uuid.NullUUID{UUID: zoneID, Valid: true},
			AlgorithmVersionID: uuid.NullUUID{UUID: verID, Valid: true},
			RuleMode:           domain.RuleModeAnomalyDetect, Title: "Smoke", Severity: domain.SeverityWarning,
			TriggeredAt: now, DetectedCount: 1, DetectionsSnapshot: json.RawMessage(`[{"label":"smoking","confidence":0.82}]`),
			RoiSnapshot: json.RawMessage(`{"name":"Full"}`), ImageObjectKey: spec.img, VideoStatus: domain.VideoStatusNone,
		})
		if cErr != nil {
			t.Fatalf("CreateAlarm: %v", cErr)
		}
		_, _ = store.Queries().HandleAlarm(ctx, sqlcgen.HandleAlarmParams{
			ID: spec.id, HandleStatus: spec.status, FalsePositiveReason: spec.reason, HandleNote: spec.note,
		})
	}

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id = $1`, camID)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteAlgorithm(cleanCtx, algoID)
		_ = store.Queries().DeleteZone(cleanCtx, zoneID)
	})

	// List MUST return ONLY fpWithImg for this zone.
	wList := doAPIReq(t, router, http.MethodGet, "/api/v1/datasets/false-positives?zone_id="+zoneID.String()+"&false_positive_reason=lighting", opTok, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("list datasets failed: status=%d body=%s", wList.Code, wList.Body.String())
	}
	var listEnv struct {
		Data struct {
			Items []api.FalsePositiveSampleItem `json:"items"`
			Total int64                         `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listEnv); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if listEnv.Data.Total != 1 || len(listEnv.Data.Items) != 1 || listEnv.Data.Items[0].AlarmID != fpWithImg {
		t.Fatalf("expected exactly 1 snapshot-bearing FP item (%s), got total=%d items=%+v", fpWithImg, listEnv.Data.Total, listEnv.Data.Items)
	}
	if listEnv.Data.Items[0].AlgorithmVersion != "3.0.0" || listEnv.Data.Items[0].CameraName != "DS-Cam-"+suffix {
		t.Fatalf("unexpected enriched metadata: %+v", listEnv.Data.Items[0])
	}

	// Export ZIP MUST contain images/<fpWithImg>.jpg and annotations.json.
	wExp := doAPIReq(t, router, http.MethodPost, "/api/v1/datasets/export", opTok, map[string]any{
		"zone_id":               zoneID.String(),
		"algorithm_version_id":  verID.String(),
		"false_positive_reason": domain.FalsePositiveLighting,
	})
	if wExp.Code != http.StatusOK {
		t.Fatalf("export dataset failed: status=%d body=%s", wExp.Code, wExp.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(wExp.Body.Bytes()), int64(wExp.Body.Len()))
	if err != nil {
		t.Fatalf("open exported zip: %v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, oErr := f.Open()
		if oErr != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, oErr)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = b
	}
	expectedImg := "images/" + fpWithImg.String() + ".jpg"
	if !bytes.Equal(files[expectedImg], jpegBytes) {
		t.Fatalf("missing or mismatched %s in zip (entries: %v)", expectedImg, len(files))
	}
	var anns []map[string]any
	if err := json.Unmarshal(files["annotations.json"], &anns); err != nil || len(anns) != 1 {
		t.Fatalf("invalid annotations.json: err=%v body=%s", err, string(files["annotations.json"]))
	}
	if anns[0]["image_file"] != expectedImg || anns[0]["false_positive_reason"] != domain.FalsePositiveLighting {
		t.Fatalf("unexpected annotation entry: %+v", anns[0])
	}
}
