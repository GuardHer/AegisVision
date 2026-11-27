package api_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func TestDashboardOverviewShapeAndZeroSafety(t *testing.T) {
	router, store, tokens := setupEnterpriseAPITest(t)
	_, tok := seedTestUserToken(t, store, tokens, domain.RoleViewer)

	// Invalid tz returns 400 cleanly.
	if wBad := doAPIReq(t, router, http.MethodGet, "/api/v1/dashboard/overview?tz=Not/A_Zone", tok, nil); wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid tz, got %d", wBad.Code)
	}

	// Seed algorithm + version + camera + ROI + rule + 4 sop_cycles (3 compliant, 1 violated)
	// to verify non-zero SOP compliance math.
	ctx := context.Background()
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	algoID := uuid.Must(uuid.NewV7())
	_, err := store.Queries().CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
		ID:              algoID,
		AlgorithmKey:    "dash_sop_" + suffix,
		Name:            "Dash SOP " + suffix,
		Description:     "test",
		SupportedLabels: json.RawMessage(`["step_a"]`),
	})
	if err != nil {
		t.Fatalf("create algorithm: %v", err)
	}
	verID := uuid.Must(uuid.NewV7())
	_, err = store.Queries().CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{
		ID:            verID,
		AlgorithmID:   algoID,
		Version:       "1.0.0",
		RuntimeKey:    "mock_rt",
		LaunchSpec:    json.RawMessage(`{}`),
		DefaultParams: json.RawMessage(`{}`),
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create algorithm version: %v", err)
	}
	camID := uuid.Must(uuid.NewV7())
	_, err = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID:         camID,
		Name:       "Dash-SOP-Cam",
		RtspUrl:    "rtsp://127.0.0.1/dash",
		StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""),
		CodecHint:  "h264",
		Status:     "online",
		Enabled:    true,
		Tags:       json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create camera: %v", err)
	}
	roiID := uuid.Must(uuid.NewV7())
	_, err = store.Queries().CreateCameraROI(ctx, sqlcgen.CreateCameraROIParams{
		ID:       roiID,
		CameraID: camID,
		Name:     "Full",
		RoiType:  domain.ROITypeFullFrame,
		Points:   json.RawMessage(`[]`),
		Enabled:  true,
		Color:    "#38bdf8",
	})
	if err != nil {
		t.Fatalf("create roi: %v", err)
	}
	ruleID := uuid.Must(uuid.NewV7())
	_, err = store.Queries().CreateROIAlgorithmRule(ctx, sqlcgen.CreateROIAlgorithmRuleParams{
		ID:                 ruleID,
		CameraID:           camID,
		RoiID:              roiID,
		AlgorithmVersionID: verID,
		Name:               "Dash SOP Rule",
		RuleMode:           domain.RuleModeSOPSequence,
		Params:             json.RawMessage(`{}`),
		AlgoParamsOverride: json.RawMessage(`{}`),
		ScheduleMode:       "auto",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	var cycleIDs []uuid.UUID
	now := time.Now().UTC()
	for _, outcome := range []string{
		domain.SOPOutcomeCompliant,
		domain.SOPOutcomeCompliant,
		domain.SOPOutcomeCompliant,
		domain.SOPOutcomeViolated,
	} {
		cid := uuid.Must(uuid.NewV7())
		_, execErr := store.Pool().Exec(ctx,
			`INSERT INTO sop_cycles (id, rule_id, camera_id, algorithm_version_id, outcome, started_at, ended_at, duration_ms)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 1500)`,
			cid, ruleID, camID, verID, outcome, now.Add(-2*time.Second), now,
		)
		if execErr != nil {
			t.Fatalf("insert sop_cycle: %v", execErr)
		}
		cycleIDs = append(cycleIDs, cid)
	}
	t.Cleanup(func() {
		cleanCtx := context.Background()
		for _, cid := range cycleIDs {
			_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM sop_cycles WHERE id = $1`, cid)
		}
		_ = store.Queries().DeleteROIAlgorithmRule(cleanCtx, ruleID)
		_ = store.Queries().DeleteCameraROI(cleanCtx, roiID)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteAlgorithm(cleanCtx, algoID)
	})

	// Review Focus #2: Overview must always return 7 contiguous trend days, non-null arrays, and finite rates.
	w := doAPIReq(t, router, http.MethodGet, "/api/v1/dashboard/overview?tz=Asia/Shanghai", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/dashboard/overview: status=%d body=%s", w.Code, w.Body.String())
	}

	var env struct {
		Code int `json:"code"`
		Data struct {
			KPIs         api.DashboardKPIs     `json:"kpis"`
			Trend7d      []api.TrendPoint      `json:"trend_7d"`
			TopZones     []api.TopZoneItem     `json:"top_zones"`
			ByRuleMode   []api.RuleModeSlice   `json:"by_rule_mode"`
			RecentAlerts []api.RecentAlertItem `json:"recent_alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal dashboard overview: %v", err)
	}
	if len(env.Data.Trend7d) != 7 {
		t.Fatalf("expected 7 days in trend_7d, got %d", len(env.Data.Trend7d))
	}
	if env.Data.TopZones == nil || env.Data.ByRuleMode == nil || env.Data.RecentAlerts == nil {
		t.Fatalf("expected non-null slices in dashboard overview: %s", w.Body.String())
	}
	if env.Data.KPIs.SOPComplianceRate <= 0 {
		t.Fatalf("expected positive sop_compliance_rate with seeded cycles, got %v", env.Data.KPIs.SOPComplianceRate)
	}
	for _, rate := range []float64{
		env.Data.KPIs.ConfirmedRate,
		env.Data.KPIs.FalsePositiveRate,
		env.Data.KPIs.SOPComplianceRate,
	} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 100 {
			t.Fatalf("invalid dashboard rate: %v", rate)
		}
	}
}
