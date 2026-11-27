package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func TestMonitorPresetsRBACAndValidation(t *testing.T) {
	router, store, tokens := setupEnterpriseAPITest(t)

	// Create real user rows because monitor_presets.owner_user_id references users(id).
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	mkUser := func(name, role string) (uuid.UUID, string) {
		u, err := store.Queries().CreateUser(context.Background(), sqlcgen.CreateUserParams{
			ID:           uuid.Must(uuid.NewV7()),
			Username:     name + "_" + suffix,
			PasswordHash: "hash",
			DisplayName:  name,
			Role:         role,
			Enabled:      true,
		})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		tok, _, _, _ := tokens.IssueAccessToken(u.ID.String(), u.Username, u.Role)
		return u.ID, tok
	}

	adminID, adminTok := mkUser("adm_p", domain.RoleAdmin)
	opID, opTok := mkUser("op_p", domain.RoleOperator)
	viewerID, viewerTok := mkUser("vw_p", domain.RoleViewer)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = store.Queries().DeleteUser(ctx, adminID)
		_ = store.Queries().DeleteUser(ctx, opID)
		_ = store.Queries().DeleteUser(ctx, viewerID)
	})

	// 1. Invalid grid_size rejected with 400
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/monitor/presets", opTok, map[string]any{
		"name":      "Bad Grid",
		"grid_size": 5,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for grid_size=5, got %d (%s)", w.Code, w.Body.String())
	}

	// 2. Review Focus #3: Non-admin creating shared preset returns 403
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/monitor/presets", opTok, map[string]any{
		"name":      "Unauthorized Shared",
		"is_shared": true,
		"grid_size": 4,
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for operator shared preset, got %d (%s)", w.Code, w.Body.String())
	}

	// 3. Operator creates private preset
	wPriv := doAPIReq(t, router, http.MethodPost, "/api/v1/monitor/presets", opTok, map[string]any{
		"name":                  "Op Private 9-Split",
		"is_shared":             false,
		"grid_size":             9,
		"camera_ids":            []string{"cam-1", "cam-2"},
		"tour_interval_seconds": 30,
	})
	if wPriv.Code != http.StatusCreated {
		t.Fatalf("create private preset: status=%d body=%s", wPriv.Code, wPriv.Body.String())
	}
	var privEnv struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wPriv.Body.Bytes(), &privEnv)

	// 4. Another user (viewer) cannot delete operator's private preset -> 403
	if w := doAPIReq(t, router, http.MethodDelete, "/api/v1/monitor/presets/"+privEnv.Data.ID.String(), viewerTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when viewer deletes operator preset, got %d", w.Code)
	}

	// 5. Admin creates shared preset -> visible to viewer, cannot be deleted by operator (403)
	wSh := doAPIReq(t, router, http.MethodPost, "/api/v1/monitor/presets", adminTok, map[string]any{
		"name":      "Duty Room 16-Split",
		"is_shared": true,
		"grid_size": 16,
	})
	if wSh.Code != http.StatusCreated {
		t.Fatalf("create shared preset: status=%d body=%s", wSh.Code, wSh.Body.String())
	}
	var shEnv struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wSh.Body.Bytes(), &shEnv)

	if w := doAPIReq(t, router, http.MethodDelete, "/api/v1/monitor/presets/"+shEnv.Data.ID.String(), opTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when operator deletes shared preset, got %d", w.Code)
	}

	wList := doAPIReq(t, router, http.MethodGet, "/api/v1/monitor/presets", viewerTok, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("list presets for viewer: status=%d", wList.Code)
	}
	var listEnv struct {
		Data []struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wList.Body.Bytes(), &listEnv)
	sawShared := false
	for _, it := range listEnv.Data {
		if it.ID == shEnv.Data.ID {
			sawShared = true
		}
		if it.ID == privEnv.Data.ID {
			t.Fatal("viewer must not see operator's private preset")
		}
	}
	if !sawShared {
		t.Fatal("viewer should see admin shared preset")
	}
}
