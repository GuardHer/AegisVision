package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func TestSchedulesCRUDDeleteGuardAndSOPRuleBinding(t *testing.T) {
	router, store, tokens := setupEnterpriseAPITest(t)
	adminTok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "admin", domain.RoleAdmin)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	ctx := context.Background()

	// 1. Invalid schedule payload rejected with 400
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/schedules", adminTok, map[string]any{
		"name":     "BadTZ-" + suffix,
		"timezone": "Invalid/Zone",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid timezone, got %d (%s)", w.Code, w.Body.String())
	}
	if w := doAPIReq(t, router, http.MethodPost, "/api/v1/schedules", adminTok, map[string]any{
		"name": "BadTime-" + suffix,
		"weekly_windows": []map[string]any{
			{"days": []int{1, 2}, "start": "25:00", "end": "08:00"},
		},
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid HH:MM, got %d (%s)", w.Code, w.Body.String())
	}

	// 2. Create overnight schedule template
	wCreate := doAPIReq(t, router, http.MethodPost, "/api/v1/schedules", adminTok, map[string]any{
		"name":        "NightShift-" + suffix,
		"description": "Factory night shift",
		"timezone":    "Asia/Shanghai",
		"weekly_windows": []map[string]any{
			{"days": []int{1, 2, 3, 4, 5}, "start": "20:00", "end": "08:00"},
		},
		"exclude_dates": []string{"2026-10-01"},
		"include_dates": []map[string]any{{"date": "2026-10-03", "start": "08:00", "end": "20:00"}},
	})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create schedule: status=%d body=%s", wCreate.Code, wCreate.Body.String())
	}
	var schedEnv struct {
		Data struct {
			ID            uuid.UUID        `json:"id"`
			WeeklyWindows []map[string]any `json:"weekly_windows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &schedEnv); err != nil {
		t.Fatalf("unmarshal schedule (JSONB must be array): %v body=%s", err, wCreate.Body.String())
	}
	schedID := schedEnv.Data.ID

	// Seed algorithm + version + camera + ROI to bind a sop_sequence rule
	algoID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
		ID: algoID, AlgorithmKey: "sop_api_" + suffix, Name: "SOP Algo " + suffix, SupportedLabels: json.RawMessage(`["wear_esd","scan"]`),
	})
	verID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{
		ID: verID, AlgorithmID: algoID, Version: "1.0.0", RuntimeKey: "mock", LaunchSpec: json.RawMessage(`{}`), DefaultParams: json.RawMessage(`{}`), Status: "active",
	})
	camID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camID, Name: "SOP-Cam-" + suffix, RtspUrl: "rtsp://127.0.0.1/s", StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""), CodecHint: "h264", Status: "online", Enabled: true, Tags: json.RawMessage(`[]`),
	})
	roiID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCameraROI(ctx, sqlcgen.CreateCameraROIParams{
		ID: roiID, CameraID: camID, Name: "Station", RoiType: domain.ROITypeFullFrame, Points: json.RawMessage(`[]`), Enabled: true, Color: "#38bdf8",
	})
	var ruleID uuid.UUID
	t.Cleanup(func() {
		cleanCtx := context.Background()
		if ruleID != uuid.Nil {
			_ = store.Queries().DeleteROIAlgorithmRule(cleanCtx, ruleID)
		}
		_ = store.Queries().DeleteCameraROI(cleanCtx, roiID)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteAlgorithm(cleanCtx, algoID)
		_ = store.Queries().DeleteArmingSchedule(cleanCtx, schedID)
	})

	// 3. Invalid sop_sequence (empty steps) rejected with 400
	if wBadSOP := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/"+camID.String()+"/rules", adminTok, map[string]any{
		"roi_id":               roiID.String(),
		"algorithm_version_id": verID.String(),
		"name":                 "Empty SOP",
		"rule_mode":            domain.RuleModeSOPSequence,
		"params":               map[string]any{"steps": []any{}},
	}); wBadSOP.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty sop steps, got %d (%s)", wBadSOP.Code, wBadSOP.Body.String())
	}

	// 4. Create valid sop_sequence rule bound to schedule_id
	wRule := doAPIReq(t, router, http.MethodPost, "/api/v1/cameras/"+camID.String()+"/rules", adminTok, map[string]any{
		"roi_id":               roiID.String(),
		"algorithm_version_id": verID.String(),
		"name":                 "ESD-Then-Scan",
		"rule_mode":            domain.RuleModeSOPSequence,
		"schedule_id":          schedID.String(),
		"params": map[string]any{
			"trigger_mode":    "first_step",
			"violation_types": []string{"missing_step", "out_of_order", "step_timeout"},
			"steps": []map[string]any{
				{"step_id": "s1", "name": "Wear ESD", "labels": []string{"wear_esd"}, "min_confidence": 0.6, "required": true},
				{"step_id": "s2", "name": "Scan", "labels": []string{"scan"}, "min_confidence": 0.6, "required": true},
			},
		},
	})
	if wRule.Code != http.StatusCreated {
		t.Fatalf("create sop_sequence rule: status=%d body=%s", wRule.Code, wRule.Body.String())
	}
	var ruleEnv struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wRule.Body.Bytes(), &ruleEnv)
	ruleID = ruleEnv.Data.ID

	// 5. Deleting referenced schedule returns 409 Conflict
	if wDel := doAPIReq(t, router, http.MethodDelete, "/api/v1/schedules/"+schedID.String(), adminTok, nil); wDel.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting referenced schedule, got %d (%s)", wDel.Code, wDel.Body.String())
	}

	// 6. Unbind rule and delete schedule -> 200 OK
	if wDelRule := doAPIReq(t, router, http.MethodDelete, "/api/v1/rules/"+ruleID.String(), adminTok, nil); wDelRule.Code != http.StatusOK {
		t.Fatalf("delete rule failed: status=%d", wDelRule.Code)
	}
	ruleID = uuid.Nil
	if wDel := doAPIReq(t, router, http.MethodDelete, "/api/v1/schedules/"+schedID.String(), adminTok, nil); wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting unreferenced schedule, got %d (%s)", wDel.Code, wDel.Body.String())
	}
}
