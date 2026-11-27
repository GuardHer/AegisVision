package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func setupEnterpriseAPITest(t *testing.T) (*gin.Engine, *postgres.Store, *auth.TokenManager) {
	t.Helper()
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
	t.Cleanup(func() { store.Close() })

	tokens := auth.NewTokenManager("test-enterprise-secret-32-chars!!", time.Hour)
	signer := auth.NewEvidenceSigner("test-enterprise-sign-32-chars!!!!")
	router := api.NewRouter(api.RouterDeps{
		Store:  store,
		Tokens: tokens,
		Signer: signer,
	})
	return router, store, tokens
}

func doAPIReq(t *testing.T, r http.Handler, method, path, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestZonesTreeHierarchyDeleteGuardAndCameraBatchImport(t *testing.T) {
	router, store, tokens := setupEnterpriseAPITest(t)
	adminTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "admin", domain.RoleAdmin)

	var createdZones []uuid.UUID
	var createdCams []uuid.UUID
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range createdCams {
			_ = store.Queries().DeleteCamera(ctx, id)
		}
		// Delete zones leaf-first
		for i := len(createdZones) - 1; i >= 0; i-- {
			_ = store.Queries().DeleteZone(ctx, createdZones[i])
		}
	})

	// 1. Create Factory -> Workshop -> Workstation hierarchy
	createZone := func(parentID, name, ztype string) uuid.UUID {
		w := doAPIReq(t, router, http.MethodPost, "/api/v1/zones", adminTok, map[string]any{
			"parent_id": parentID,
			"name":      name,
			"zone_type": ztype,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create zone %s: status=%d body=%s", name, w.Code, w.Body.String())
		}
		var env struct {
			Data struct {
				ID uuid.UUID `json:"id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		createdZones = append(createdZones, env.Data.ID)
		return env.Data.ID
	}

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	facID := createZone("", "Factory-"+suffix, domain.ZoneTypeFactory)
	wsID := createZone(facID.String(), "Workshop-"+suffix, domain.ZoneTypeWorkshop)
	stID := createZone(wsID.String(), "Station-"+suffix, domain.ZoneTypeWorkstation)

	// 2. Bind 2 cameras (1 online, 1 offline) to Station via CSV import
	csvBody := "name,rtsp_url,sub_stream_url,workstation_code,zone_id,codec_hint\n" +
		"Cam1-" + suffix + ",rtsp://127.0.0.1/c1,,WS-01," + stID.String() + ",h264\n" +
		"Cam2-" + suffix + ",rtsp://127.0.0.1/c2,,WS-02," + stID.String() + ",h264\n" +
		",rtsp://invalid-missing-name,,,,\n"
	wImp := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/import", adminTok, map[string]any{"csv": csvBody})
	if wImp.Code != http.StatusOK {
		t.Fatalf("import csv: status=%d body=%s", wImp.Code, wImp.Body.String())
	}
	var impResp struct {
		Data struct {
			CreatedCount int              `json:"created_count"`
			Cameras      []sqlcgen.Camera `json:"cameras"`
			Errors       []string         `json:"errors"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wImp.Body.Bytes(), &impResp)
	if impResp.Data.CreatedCount != 2 || len(impResp.Data.Errors) != 1 {
		t.Fatalf("unexpected import result: created=%d errors=%v", impResp.Data.CreatedCount, impResp.Data.Errors)
	}
	for _, c := range impResp.Data.Cameras {
		createdCams = append(createdCams, c.ID)
	}
	if err := store.Queries().UpdateCameraStatus(context.Background(), sqlcgen.UpdateCameraStatusParams{
		ID:     createdCams[0],
		Status: "online",
	}); err != nil {
		t.Fatalf("UpdateCameraStatus: %v", err)
	}

	// 3. Verify GET /api/v1/zones/tree rolls up camera_count=2, online_camera_count=1 to Factory
	wTree := doAPIReq(t, router, http.MethodGet, "/api/v1/zones/tree", adminTok, nil)
	if wTree.Code != http.StatusOK {
		t.Fatalf("get zones tree: status=%d body=%s", wTree.Code, wTree.Body.String())
	}
	var treeResp struct {
		Data []*api.ZoneTreeNode `json:"data"`
	}
	_ = json.Unmarshal(wTree.Body.Bytes(), &treeResp)
	var foundFac *api.ZoneTreeNode
	for _, n := range treeResp.Data {
		if n.ID == facID {
			foundFac = n
			break
		}
	}
	if foundFac == nil || foundFac.CameraCount != 2 || foundFac.OnlineCameraCount != 1 {
		t.Fatalf("expected factory rolled-up counts (2, 1), got %+v", foundFac)
	}

	// 4. Review Focus #1: Delete protection (409 Conflict) on non-empty zones
	if w := doAPIReq(t, router, http.MethodDelete, "/api/v1/zones/"+facID.String(), adminTok, nil); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting zone with children, got %d (%s)", w.Code, w.Body.String())
	}
	if w := doAPIReq(t, router, http.MethodDelete, "/api/v1/zones/"+stID.String(), adminTok, nil); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting zone with bound cameras, got %d (%s)", w.Code, w.Body.String())
	}

	// 5. Batch move cameras out of Station & disable them -> then Station can be deleted cleanly
	wBatch := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/batch", adminTok, map[string]any{
		"camera_ids": []string{createdCams[0].String(), createdCams[1].String()},
		"action":     "move_zone",
		"zone_id":    "",
	})
	if wBatch.Code != http.StatusOK {
		t.Fatalf("batch move_zone: status=%d body=%s", wBatch.Code, wBatch.Body.String())
	}
	wDis := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/batch", adminTok, map[string]any{
		"camera_ids": []string{createdCams[0].String()},
		"action":     "disable",
	})
	if wDis.Code != http.StatusOK {
		t.Fatalf("batch disable: status=%d body=%s", wDis.Code, wDis.Body.String())
	}
	if w := doAPIReq(t, router, http.MethodDelete, "/api/v1/zones/"+stID.String(), adminTok, nil); w.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting emptied station zone, got %d (%s)", w.Code, w.Body.String())
	}
}
