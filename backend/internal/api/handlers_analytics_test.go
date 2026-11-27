package api_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func TestAnalyticsSummaryZeroSafetySOPComplianceAndCSVExport(t *testing.T) {
	router, store, tokens := setupEnterpriseAPITest(t)
	_, tok := seedTestUserToken(t, store, tokens, domain.RoleViewer)
	ctx := context.Background()

	// 1. Review Focus #5: Empty future range returns finite 0.0 rates & non-null arrays.
	futureStart := url.QueryEscape("2099-01-01T00:00:00Z")
	futureEnd := url.QueryEscape("2099-01-02T00:00:00Z")
	wEmpty := doAPIReq(t, router, http.MethodGet, "/api/v1/analytics/summary?start_time="+futureStart+"&end_time="+futureEnd, tok, nil)
	if wEmpty.Code != http.StatusOK {
		t.Fatalf("empty analytics summary: status=%d body=%s", wEmpty.Code, wEmpty.Body.String())
	}
	var emptyEnv struct {
		Data api.AnalyticsSummaryPayload `json:"data"`
	}
	if err := json.Unmarshal(wEmpty.Body.Bytes(), &emptyEnv); err != nil {
		t.Fatalf("unmarshal empty analytics: %v", err)
	}
	if emptyEnv.Data.Trend == nil || emptyEnv.Data.ByZone == nil || emptyEnv.Data.TopSOPSteps == nil || emptyEnv.Data.AlgoPrecision == nil {
		t.Fatalf("expected non-null arrays in empty analytics: %s", wEmpty.Body.String())
	}
	if math.IsNaN(emptyEnv.Data.KPIs.SOPComplianceRate) || math.IsNaN(emptyEnv.Data.KPIs.FalsePositiveRate) {
		t.Fatal("rates must never be NaN")
	}

	// 2. Seed zone (with formula-prefix name for CSV injection test), algo, camera, rule, alarms, and 4 SOP cycles (3 compliant, 1 violated)
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	zoneID := uuid.Must(uuid.NewV7())
	_, err := store.Queries().CreateZone(ctx, sqlcgen.CreateZoneParams{
		ID: zoneID, Name: "=Workshop-" + suffix, ZoneType: domain.ZoneTypeWorkshop,
	})
	if err != nil {
		t.Fatalf("create zone: %v", err)
	}
	algoID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithm(ctx, sqlcgen.CreateAlgorithmParams{
		ID: algoID, AlgorithmKey: "an_algo_" + suffix, Name: "AnAlgo-" + suffix, SupportedLabels: json.RawMessage(`["smoking"]`),
	})
	verID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateAlgorithmVersion(ctx, sqlcgen.CreateAlgorithmVersionParams{
		ID: verID, AlgorithmID: algoID, Version: "2.1.0", RuntimeKey: "mock", LaunchSpec: json.RawMessage(`{}`), DefaultParams: json.RawMessage(`{}`), Status: "active",
	})
	camID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
		ID: camID, Name: "AnCam-" + suffix, RtspUrl: "rtsp://127.0.0.1/an", StreamPath: "cam_" + strings.ReplaceAll(camID.String(), "-", ""), CodecHint: "h264", Status: "online", Enabled: true, ZoneID: uuid.NullUUID{UUID: zoneID, Valid: true}, Tags: json.RawMessage(`[]`),
	})
	roiID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateCameraROI(ctx, sqlcgen.CreateCameraROIParams{
		ID: roiID, CameraID: camID, Name: "Full", RoiType: domain.ROITypeFullFrame, Points: json.RawMessage(`[]`), Enabled: true, Color: "#38bdf8",
	})
	ruleID := uuid.Must(uuid.NewV7())
	_, _ = store.Queries().CreateROIAlgorithmRule(ctx, sqlcgen.CreateROIAlgorithmRuleParams{
		ID: ruleID, CameraID: camID, RoiID: roiID, AlgorithmVersionID: verID, Name: "SOPRule", RuleMode: domain.RuleModeSOPSequence, Params: json.RawMessage(`{}`), AlgoParamsOverride: json.RawMessage(`{}`), ScheduleMode: "auto", Enabled: true,
	})

	now := time.Now().UTC()
	// 1 confirmed safety alarm + 1 false_positive safety alarm + 1 SOP alarm with sop_trace
	aSafe1 := uuid.Must(uuid.NewV7())
	aSafe2 := uuid.Must(uuid.NewV7())
	aSOP := uuid.Must(uuid.NewV7())
	for _, spec := range []struct {
		id     uuid.UUID
		mode   string
		status string
		trace  json.RawMessage
	}{
		{aSafe1, domain.RuleModeAnomalyDetect, domain.HandleStatusConfirmed, nil},
		{aSafe2, domain.RuleModeAnomalyDetect, domain.HandleStatusFalsePositive, nil},
		{aSOP, domain.RuleModeSOPSequence, domain.HandleStatusConfirmed, json.RawMessage(`{"violation_type":"missing_step","violated_step_name":"=WearESD"}`)},
	} {
		_, cErr := store.Queries().CreateAlarm(ctx, sqlcgen.CreateAlarmParams{
			ID: spec.id, CameraID: camID, ZoneID: uuid.NullUUID{UUID: zoneID, Valid: true}, RoiID: uuid.NullUUID{UUID: roiID, Valid: true},
			RuleID: uuid.NullUUID{UUID: ruleID, Valid: true}, AlgorithmVersionID: uuid.NullUUID{UUID: verID, Valid: true},
			RuleMode: spec.mode, Title: "AnAlarm", Severity: domain.SeverityWarning, TriggeredAt: now,
			DetectedCount: 1, DetectionsSnapshot: json.RawMessage(`[]`), RoiSnapshot: json.RawMessage(`{}`),
			VideoStatus: domain.VideoStatusNone, SopTrace: spec.trace,
		})
		if cErr != nil {
			t.Fatalf("create alarm: %v", cErr)
		}
		_, _ = store.Queries().HandleAlarm(ctx, sqlcgen.HandleAlarmParams{ID: spec.id, HandleStatus: spec.status})
	}

	for _, outcome := range []string{domain.SOPOutcomeCompliant, domain.SOPOutcomeCompliant, domain.SOPOutcomeCompliant, domain.SOPOutcomeViolated} {
		_, _ = store.Queries().CreateSOPCycle(ctx, sqlcgen.CreateSOPCycleParams{
			ID: uuid.Must(uuid.NewV7()), RuleID: ruleID, CameraID: camID, ZoneID: uuid.NullUUID{UUID: zoneID, Valid: true},
			AlgorithmVersionID: uuid.NullUUID{UUID: verID, Valid: true}, Outcome: outcome, StartedAt: now.Add(-2 * time.Second), EndedAt: now, DurationMs: 2000,
		})
	}

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM sop_cycles WHERE camera_id = $1`, camID)
		_, _ = store.Pool().Exec(cleanCtx, `DELETE FROM alarms WHERE camera_id = $1`, camID)
		_ = store.Queries().DeleteROIAlgorithmRule(cleanCtx, ruleID)
		_ = store.Queries().DeleteCameraROI(cleanCtx, roiID)
		_ = store.Queries().DeleteCamera(cleanCtx, camID)
		_ = store.Queries().DeleteAlgorithm(cleanCtx, algoID)
		_ = store.Queries().DeleteZone(cleanCtx, zoneID)
	})

	qs := "?zone_id=" + zoneID.String() + "&granularity=hour&shift=all&tz=Asia/Shanghai"
	wSum := doAPIReq(t, router, http.MethodGet, "/api/v1/analytics/summary"+qs, tok, nil)
	if wSum.Code != http.StatusOK {
		t.Fatalf("analytics summary: status=%d body=%s", wSum.Code, wSum.Body.String())
	}
	var sumEnv struct {
		Data api.AnalyticsSummaryPayload `json:"data"`
	}
	if err := json.Unmarshal(wSum.Body.Bytes(), &sumEnv); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if sumEnv.Data.KPIs.SOPComplianceRate != 75.0 || sumEnv.Data.KPIs.AvgSOPDurationMs != 2000 {
		t.Fatalf("expected sop_compliance_rate=75.0 and avg=2000ms, got %+v", sumEnv.Data.KPIs)
	}
	if len(sumEnv.Data.TopSOPSteps) != 1 || sumEnv.Data.TopSOPSteps[0].StepName != "=WearESD" {
		t.Fatalf("expected top SOP violated step =WearESD, got %+v", sumEnv.Data.TopSOPSteps)
	}
	if len(sumEnv.Data.AlgoPrecision) != 1 || sumEnv.Data.AlgoPrecision[0].TotalAlarms != 3 {
		t.Fatalf("expected algo precision total=3, got %+v", sumEnv.Data.AlgoPrecision)
	}

	// 3. Review Focus #5: CSV export has UTF-8 BOM and neutralizes formula cells
	wExp := doAPIReq(t, router, http.MethodGet, "/api/v1/analytics/export"+qs, tok, nil)
	if wExp.Code != http.StatusOK || !strings.Contains(wExp.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("analytics export failed: status=%d ct=%q", wExp.Code, wExp.Header().Get("Content-Type"))
	}
	csvBody := wExp.Body.String()
	if !strings.Contains(csvBody, "'=Workshop-"+suffix) || !strings.Contains(csvBody, "'=WearESD") {
		t.Fatalf("expected formula cells neutralized in analytics CSV, got: %s", csvBody)
	}
}
