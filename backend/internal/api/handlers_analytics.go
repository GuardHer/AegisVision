package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	// Embed IANA timezone database so analytics timezone parsing works on Windows and minimal containers.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// AnalyticsHandler serves enterprise safety & SOP reporting summaries and CSV exports.
type AnalyticsHandler struct {
	store *postgres.Store
	log   *slog.Logger
}

// NewAnalyticsHandler constructs an AnalyticsHandler.
func NewAnalyticsHandler(store *postgres.Store, log *slog.Logger) *AnalyticsHandler {
	return &AnalyticsHandler{store: store, log: log}
}

// AnalyticsKPIs summarizes period-level alarm totals, SOP compliance, and cycle latency.
type AnalyticsKPIs struct {
	TotalAlarms        int64   `json:"total_alarms"`
	SafetyAlarms       int64   `json:"safety_alarms"`
	SOPAlarms          int64   `json:"sop_alarms"`
	ConfirmedCount     int64   `json:"confirmed_count"`
	FalsePositiveCount int64   `json:"false_positive_count"`
	FalsePositiveRate  float64 `json:"false_positive_rate"`
	SOPCompliantCycles int64   `json:"sop_compliant_cycles"`
	SOPViolatedCycles  int64   `json:"sop_violated_cycles"`
	SOPComplianceRate  float64 `json:"sop_compliance_rate"`
	AvgSOPDurationMs   int64   `json:"avg_sop_duration_ms"`
}

// AnalyticsTrendPoint represents one day or hour bucket of safety vs. SOP alarms.
type AnalyticsTrendPoint struct {
	Bucket      string `json:"bucket"`
	SafetyCount int64  `json:"safety_count"`
	SOPCount    int64  `json:"sop_count"`
}

// AnalyticsZoneRow compares safety vs. SOP alarm counts per zone.
type AnalyticsZoneRow struct {
	ZoneID             *uuid.UUID `json:"zone_id"`
	ZoneName           string     `json:"zone_name"`
	SafetyCount        int64      `json:"safety_count"`
	SOPCount           int64      `json:"sop_count"`
	ConfirmedCount     int64      `json:"confirmed_count"`
	FalsePositiveCount int64      `json:"false_positive_count"`
}

// AnalyticsSOPStepRow ranks the most frequently violated SOP steps.
type AnalyticsSOPStepRow struct {
	StepName      string `json:"step_name"`
	ViolationType string `json:"violation_type"`
	Count         int64  `json:"count"`
}

// AnalyticsAlgoPrecisionRow reports trigger count and false-positive rate per algorithm version.
type AnalyticsAlgoPrecisionRow struct {
	AlgorithmVersionID uuid.UUID `json:"algorithm_version_id"`
	AlgorithmName      string    `json:"algorithm_name"`
	Version            string    `json:"version"`
	TotalAlarms        int64     `json:"total_alarms"`
	ConfirmedCount     int64     `json:"confirmed_count"`
	FalsePositiveCount int64     `json:"false_positive_count"`
	FalsePositiveRate  float64   `json:"false_positive_rate"`
}

// AnalyticsSummaryPayload bundles all analytics sections for JSON and CSV rendering.
type AnalyticsSummaryPayload struct {
	KPIs          AnalyticsKPIs               `json:"kpis"`
	Trend         []AnalyticsTrendPoint       `json:"trend"`
	ByZone        []AnalyticsZoneRow          `json:"by_zone"`
	TopSOPSteps   []AnalyticsSOPStepRow       `json:"top_sop_steps"`
	AlgoPrecision []AnalyticsAlgoPrecisionRow `json:"algo_precision"`
}

type analyticsFilter struct {
	startTime   time.Time
	endTime     time.Time
	zoneIDs     []uuid.UUID
	granularity string
	shift       string
	tzName      string
}

func (h *AnalyticsHandler) parseFilter(c *gin.Context) (analyticsFilter, error) {
	tzName := strings.TrimSpace(c.Query("tz"))
	if tzName == "" || strings.EqualFold(tzName, "Local") {
		tzName = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return analyticsFilter{}, domain.NewInvalidArgumentError("invalid tz timezone", err)
	}

	nowLocal := time.Now().In(loc)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	startTime := todayStart.AddDate(0, 0, -6).UTC()
	endTime := todayStart.AddDate(0, 0, 1).UTC()

	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		t, pErr := time.Parse(time.RFC3339, raw)
		if pErr != nil {
			return analyticsFilter{}, domain.NewInvalidArgumentError("start_time must be RFC3339", pErr)
		}
		startTime = t.UTC()
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		t, pErr := time.Parse(time.RFC3339, raw)
		if pErr != nil {
			return analyticsFilter{}, domain.NewInvalidArgumentError("end_time must be RFC3339", pErr)
		}
		endTime = t.UTC()
	}
	if !endTime.After(startTime) {
		return analyticsFilter{}, domain.NewInvalidArgumentError("end_time must be after start_time", nil)
	}

	granularity := strings.ToLower(strings.TrimSpace(c.DefaultQuery("granularity", "day")))
	if granularity != "day" && granularity != "hour" {
		return analyticsFilter{}, domain.NewInvalidArgumentError("granularity must be day or hour", nil)
	}

	shift := strings.ToLower(strings.TrimSpace(c.DefaultQuery("shift", "all")))
	if shift != "all" && shift != "day" && shift != "night" {
		return analyticsFilter{}, domain.NewInvalidArgumentError("shift must be all, day, or night", nil)
	}

	var zoneIDs []uuid.UUID
	if rawZone := strings.TrimSpace(c.Query("zone_id")); rawZone != "" {
		zid, pErr := uuid.Parse(rawZone)
		if pErr != nil {
			return analyticsFilter{}, domain.NewInvalidArgumentError("invalid zone_id", pErr)
		}
		desc, descErr := collectZoneDescendants(c.Request.Context(), h.store.Queries(), zid)
		if descErr != nil {
			return analyticsFilter{}, descErr
		}
		zoneIDs = desc
	}

	return analyticsFilter{
		startTime:   startTime,
		endTime:     endTime,
		zoneIDs:     zoneIDs,
		granularity: granularity,
		shift:       shift,
		tzName:      tzName,
	}, nil
}

func (h *AnalyticsHandler) computeSummary(ctx context.Context, f analyticsFilter) (AnalyticsSummaryPayload, error) {
	q := h.store.Queries()
	trend := make([]AnalyticsTrendPoint, 0)
	if f.granularity == "hour" {
		rows, err := q.GetAnalyticsTrendByHour(ctx, sqlcgen.GetAnalyticsTrendByHourParams{
			Tz:        f.tzName,
			StartTime: f.startTime,
			EndTime:   f.endTime,
			ZoneIds:   f.zoneIDs,
			Shift:     f.shift,
		})
		if err != nil {
			return AnalyticsSummaryPayload{}, err
		}
		for _, r := range rows {
			trend = append(trend, AnalyticsTrendPoint{Bucket: r.Bucket, SafetyCount: r.SafetyCount, SOPCount: r.SopCount})
		}
	} else {
		rows, err := q.GetAnalyticsTrendByDay(ctx, sqlcgen.GetAnalyticsTrendByDayParams{
			Tz:        f.tzName,
			StartTime: f.startTime,
			EndTime:   f.endTime,
			ZoneIds:   f.zoneIDs,
			Shift:     f.shift,
		})
		if err != nil {
			return AnalyticsSummaryPayload{}, err
		}
		for _, r := range rows {
			trend = append(trend, AnalyticsTrendPoint{Bucket: r.Bucket, SafetyCount: r.SafetyCount, SOPCount: r.SopCount})
		}
	}

	zoneRows, err := q.GetAnalyticsByZone(ctx, sqlcgen.GetAnalyticsByZoneParams{
		Tz:        f.tzName,
		StartTime: f.startTime,
		EndTime:   f.endTime,
		ZoneIds:   f.zoneIDs,
		Shift:     f.shift,
	})
	if err != nil {
		return AnalyticsSummaryPayload{}, err
	}
	byZone := make([]AnalyticsZoneRow, 0, len(zoneRows))
	var safetyTotal, sopTotal, confirmedTotal, fpTotal int64
	for _, z := range zoneRows {
		var zid *uuid.UUID
		if z.ZoneID.Valid {
			v := z.ZoneID.UUID
			zid = &v
		}
		safetyTotal += z.SafetyCount
		sopTotal += z.SopCount
		confirmedTotal += z.ConfirmedCount
		fpTotal += z.FalsePositiveCount
		byZone = append(byZone, AnalyticsZoneRow{
			ZoneID:             zid,
			ZoneName:           z.ZoneName,
			SafetyCount:        z.SafetyCount,
			SOPCount:           z.SopCount,
			ConfirmedCount:     z.ConfirmedCount,
			FalsePositiveCount: z.FalsePositiveCount,
		})
	}

	sopRow, err := q.GetAnalyticsSOPSummary(ctx, sqlcgen.GetAnalyticsSOPSummaryParams{
		Tz:        f.tzName,
		StartTime: f.startTime,
		EndTime:   f.endTime,
		ZoneIds:   f.zoneIDs,
		Shift:     f.shift,
	})
	if err != nil {
		return AnalyticsSummaryPayload{}, err
	}

	stepRows, err := q.GetAnalyticsTopSOPViolatedSteps(ctx, sqlcgen.GetAnalyticsTopSOPViolatedStepsParams{
		Tz:        f.tzName,
		StartTime: f.startTime,
		EndTime:   f.endTime,
		ZoneIds:   f.zoneIDs,
		Shift:     f.shift,
	})
	if err != nil {
		return AnalyticsSummaryPayload{}, err
	}
	topSteps := make([]AnalyticsSOPStepRow, 0, len(stepRows))
	for _, s := range stepRows {
		topSteps = append(topSteps, AnalyticsSOPStepRow{
			StepName:      s.StepName,
			ViolationType: s.ViolationType,
			Count:         s.Count,
		})
	}

	algoRows, err := q.GetAnalyticsAlgoPrecision(ctx, sqlcgen.GetAnalyticsAlgoPrecisionParams{
		Tz:        f.tzName,
		StartTime: f.startTime,
		EndTime:   f.endTime,
		ZoneIds:   f.zoneIDs,
		Shift:     f.shift,
	})
	if err != nil {
		return AnalyticsSummaryPayload{}, err
	}
	algoPrecision := make([]AnalyticsAlgoPrecisionRow, 0, len(algoRows))
	for _, a := range algoRows {
		algoPrecision = append(algoPrecision, AnalyticsAlgoPrecisionRow{
			AlgorithmVersionID: a.AlgorithmVersionID,
			AlgorithmName:      a.AlgorithmName,
			Version:            a.Version,
			TotalAlarms:        a.TotalAlarms,
			ConfirmedCount:     a.ConfirmedCount,
			FalsePositiveCount: a.FalsePositiveCount,
			FalsePositiveRate:  safePercent(a.FalsePositiveCount, a.HandledCount),
		})
	}

	kpis := AnalyticsKPIs{
		TotalAlarms:        safetyTotal + sopTotal,
		SafetyAlarms:       safetyTotal,
		SOPAlarms:          sopTotal,
		ConfirmedCount:     confirmedTotal,
		FalsePositiveCount: fpTotal,
		FalsePositiveRate:  safePercent(fpTotal, confirmedTotal+fpTotal),
		SOPCompliantCycles: sopRow.CompliantCount,
		SOPViolatedCycles:  sopRow.ViolatedCount,
		SOPComplianceRate:  safePercent(sopRow.CompliantCount, sopRow.CompliantCount+sopRow.ViolatedCount),
		AvgSOPDurationMs:   sopRow.AvgDurationMs,
	}

	return AnalyticsSummaryPayload{
		KPIs:          kpis,
		Trend:         trend,
		ByZone:        byZone,
		TopSOPSteps:   topSteps,
		AlgoPrecision: algoPrecision,
	}, nil
}

// Summary returns aggregated enterprise safety, SOP compliance, and algorithm precision analytics.
func (h *AnalyticsHandler) Summary(c *gin.Context) {
	f, err := h.parseFilter(c)
	if err != nil {
		respondErr(c, err)
		return
	}
	payload, err := h.computeSummary(c.Request.Context(), f)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, payload)
}

// Export streams a UTF-8 BOM CSV summary report covering trend, zones, top SOP violated steps, and algorithm precision.
func (h *AnalyticsHandler) Export(c *gin.Context) {
	f, err := h.parseFilter(c)
	if err != nil {
		respondErr(c, err)
		return
	}
	payload, err := h.computeSummary(c.Request.Context(), f)
	if err != nil {
		respondErr(c, err)
		return
	}

	tsTag := time.Now().UTC().Format("20060102-150405")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="aegis-analytics-%s.csv"`, tsTag))
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"section", "dimension", "metric_a", "metric_b", "metric_c", "rate_pct"})
	for _, pt := range payload.Trend {
		_ = w.Write([]string{"trend", sanitizeCSVCell(pt.Bucket), strconv.FormatInt(pt.SafetyCount, 10), strconv.FormatInt(pt.SOPCount, 10), "", ""})
	}
	for _, z := range payload.ByZone {
		_ = w.Write([]string{"zone", sanitizeCSVCell(z.ZoneName), strconv.FormatInt(z.SafetyCount, 10), strconv.FormatInt(z.SOPCount, 10), strconv.FormatInt(z.FalsePositiveCount, 10), ""})
	}
	for _, s := range payload.TopSOPSteps {
		_ = w.Write([]string{"sop_step", sanitizeCSVCell(s.StepName), sanitizeCSVCell(s.ViolationType), strconv.FormatInt(s.Count, 10), "", ""})
	}
	for _, a := range payload.AlgoPrecision {
		_ = w.Write([]string{
			"algorithm_version",
			sanitizeCSVCell(a.AlgorithmName + " " + a.Version),
			strconv.FormatInt(a.TotalAlarms, 10),
			strconv.FormatInt(a.ConfirmedCount, 10),
			strconv.FormatInt(a.FalsePositiveCount, 10),
			fmt.Sprintf("%.1f", a.FalsePositiveRate),
		})
	}
	w.Flush()
	if wErr := w.Error(); wErr != nil && h.log != nil {
		h.log.Warn("analytics csv export stream error", "err", wErr)
	}
}
