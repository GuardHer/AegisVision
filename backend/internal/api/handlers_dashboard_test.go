package api_test

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/domain"
)

func TestDashboardOverviewShapeAndZeroSafety(t *testing.T) {
	router, _, tokens := setupEnterpriseAPITest(t)
	tok, _, _, _ := tokens.IssueAccessToken(uuid.Must(uuid.NewV7()).String(), "viewer", domain.RoleViewer)

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
