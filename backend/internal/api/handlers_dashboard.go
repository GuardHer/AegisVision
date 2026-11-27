package api

import (
	"math"
	"strings"
	"time"
	// Embed IANA timezone database so time.LoadLocation works on Windows and minimal containers without CGO.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// DashboardHandler serves enterprise operations overview KPIs and offline ECharts datasets.
type DashboardHandler struct {
	store *postgres.Store
}

// NewDashboardHandler constructs a DashboardHandler.
func NewDashboardHandler(store *postgres.Store) *DashboardHandler {
	return &DashboardHandler{store: store}
}

// DashboardKPIs summarizes daily safety, SOP, camera, and compute cluster indicators.
type DashboardKPIs struct {
	TodayAlarms        int64   `json:"today_alarms"`
	YesterdayAlarms    int64   `json:"yesterday_alarms"`
	UnhandledAlarms    int64   `json:"unhandled_alarms"`
	ConfirmedRate      float64 `json:"confirmed_rate"`
	FalsePositiveRate  float64 `json:"false_positive_rate"`
	TodaySOPViolations int64   `json:"today_sop_violations"`
	SOPComplianceRate  float64 `json:"sop_compliance_rate"`
	CamerasOnline      int64   `json:"cameras_online"`
	CamerasTotal       int64   `json:"cameras_total"`
	NodesOnline        int64   `json:"nodes_online"`
	NodesTotal         int64   `json:"nodes_total"`
	RunningTasks       int64   `json:"running_tasks"`
}

// TrendPoint represents one calendar day of safety vs. SOP alarm volume.
type TrendPoint struct {
	Date        string `json:"date"`
	SafetyCount int64  `json:"safety_count"`
	SOPCount    int64  `json:"sop_count"`
}

// TopZoneItem ranks a spatial zone by recent alarm volume.
type TopZoneItem struct {
	ZoneID     *uuid.UUID `json:"zone_id"`
	ZoneName   string     `json:"zone_name"`
	AlarmCount int64      `json:"alarm_count"`
}

// RuleModeSlice aggregates alarms by rule_mode.
type RuleModeSlice struct {
	RuleMode string `json:"rule_mode"`
	Count    int64  `json:"count"`
}

// RecentAlertItem is a concise high-severity unhandled alert row for the dashboard feed.
type RecentAlertItem struct {
	ID             uuid.UUID  `json:"id"`
	CameraID       uuid.UUID  `json:"camera_id"`
	CameraName     string     `json:"camera_name"`
	ZoneID         *uuid.UUID `json:"zone_id"`
	ZoneName       string     `json:"zone_name"`
	RuleMode       string     `json:"rule_mode"`
	Title          string     `json:"title"`
	Severity       string     `json:"severity"`
	HandleStatus   string     `json:"handle_status"`
	ImageObjectKey string     `json:"image_object_key"`
	TriggeredAt    time.Time  `json:"triggered_at"`
}

// Overview returns aggregated factory KPIs, 7-day trend, top-10 zones, rule-mode breakdown, and recent alerts.
func (h *DashboardHandler) Overview(c *gin.Context) {
	ctx := c.Request.Context()
	q := h.store.Queries()

	scope, scopeErr := resolveRequestScope(c, h.store)
	if scopeErr != nil {
		respondErr(c, scopeErr)
		return
	}
	scopeZones, emptyScope := scope.IntersectZoneFilter(nil)

	tzName := strings.TrimSpace(c.Query("tz"))
	if tzName == "" || strings.EqualFold(tzName, "Local") {
		tzName = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid timezone parameter", err))
		return
	}

	nowLocal := time.Now().In(loc)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	since7d := todayStart.AddDate(0, 0, -6)

	nodeHealth, err := q.GetDashboardNodeHealth(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	if emptyScope {
		trend7d := make([]TrendPoint, 0, 7)
		for i := 0; i < 7; i++ {
			trend7d = append(trend7d, TrendPoint{Date: since7d.AddDate(0, 0, i).Format("2006-01-02")})
		}
		respondOK(c, gin.H{
			"kpis": DashboardKPIs{
				NodesOnline:  nodeHealth.OnlineNodes,
				NodesTotal:   nodeHealth.TotalNodes,
				RunningTasks: nodeHealth.RunningTasks,
			},
			"trend_7d":      trend7d,
			"top_zones":     []TopZoneItem{},
			"by_rule_mode":  []RuleModeSlice{},
			"recent_alerts": []RecentAlertItem{},
		})
		return
	}

	kpiRow, err := q.GetDashboardAlarmKPIs(ctx, sqlcgen.GetDashboardAlarmKPIsParams{
		TodayStart:     todayStart.UTC(),
		TomorrowStart:  tomorrowStart.UTC(),
		YesterdayStart: yesterdayStart.UTC(),
		ZoneIds:        scopeZones,
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	sopRow, err := q.GetDashboardSOPCycleCounts(ctx, sqlcgen.GetDashboardSOPCycleCountsParams{
		SinceTime: since7d.UTC(),
		ZoneIds:   scopeZones,
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	camHealth, err := q.GetDashboardCameraHealth(ctx, scopeZones)
	if err != nil {
		respondErr(c, err)
		return
	}

	rawTrend, err := q.GetAlarmTrendByDay(ctx, sqlcgen.GetAlarmTrendByDayParams{
		Tz:        tzName,
		SinceTime: since7d.UTC(),
		ZoneIds:   scopeZones,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	trendByDay := make(map[string]sqlcgen.GetAlarmTrendByDayRow, len(rawTrend))
	for _, r := range rawTrend {
		trendByDay[r.Day] = r
	}
	trend7d := make([]TrendPoint, 0, 7)
	for i := 0; i < 7; i++ {
		dayKey := since7d.AddDate(0, 0, i).Format("2006-01-02")
		pt := TrendPoint{Date: dayKey}
		if r, ok := trendByDay[dayKey]; ok {
			pt.SafetyCount = r.SafetyCount
			pt.SOPCount = r.SopCount
		}
		trend7d = append(trend7d, pt)
	}

	rawZones, err := q.GetTopZonesByAlarms(ctx, sqlcgen.GetTopZonesByAlarmsParams{
		SinceTime: since7d.UTC(),
		ZoneIds:   scopeZones,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	topZones := make([]TopZoneItem, 0, len(rawZones))
	for _, z := range rawZones {
		var zid *uuid.UUID
		if z.ZoneID.Valid {
			v := z.ZoneID.UUID
			zid = &v
		}
		topZones = append(topZones, TopZoneItem{
			ZoneID:     zid,
			ZoneName:   z.ZoneName,
			AlarmCount: z.AlarmCount,
		})
	}

	rawModes, err := q.GetAlarmsByRuleMode(ctx, sqlcgen.GetAlarmsByRuleModeParams{
		SinceTime: since7d.UTC(),
		ZoneIds:   scopeZones,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	byRuleMode := make([]RuleModeSlice, 0, len(rawModes))
	for _, m := range rawModes {
		byRuleMode = append(byRuleMode, RuleModeSlice{
			RuleMode: m.RuleMode,
			Count:    m.Count,
		})
	}

	rawRecent, err := q.ListRecentHighSeverityAlarms(ctx, scopeZones)
	if err != nil {
		respondErr(c, err)
		return
	}
	recentAlerts := make([]RecentAlertItem, 0, len(rawRecent))
	for _, a := range rawRecent {
		var zid *uuid.UUID
		if a.ZoneID.Valid {
			v := a.ZoneID.UUID
			zid = &v
		}
		recentAlerts = append(recentAlerts, RecentAlertItem{
			ID:             a.ID,
			CameraID:       a.CameraID,
			CameraName:     a.CameraName,
			ZoneID:         zid,
			ZoneName:       a.ZoneName,
			RuleMode:       a.RuleMode,
			Title:          a.Title,
			Severity:       a.Severity,
			HandleStatus:   a.HandleStatus,
			ImageObjectKey: a.ImageObjectKey,
			TriggeredAt:    a.TriggeredAt,
		})
	}

	kpis := DashboardKPIs{
		TodayAlarms:        kpiRow.TodayAlarms,
		YesterdayAlarms:    kpiRow.YesterdayAlarms,
		UnhandledAlarms:    kpiRow.UnhandledAlarms,
		ConfirmedRate:      safePercent(kpiRow.TodayConfirmed, kpiRow.TodayHandled),
		FalsePositiveRate:  safePercent(kpiRow.TodayFalsePositive, kpiRow.TodayHandled),
		TodaySOPViolations: kpiRow.TodaySopViolations,
		SOPComplianceRate:  safePercent(sopRow.CompliantCount, sopRow.CompliantCount+sopRow.ViolatedCount),
		CamerasOnline:      camHealth.OnlineCount,
		CamerasTotal:       camHealth.TotalCount,
		NodesOnline:        nodeHealth.OnlineNodes,
		NodesTotal:         nodeHealth.TotalNodes,
		RunningTasks:       nodeHealth.RunningTasks,
	}

	respondOK(c, gin.H{
		"kpis":          kpis,
		"trend_7d":      trend7d,
		"top_zones":     topZones,
		"by_rule_mode":  byRuleMode,
		"recent_alerts": recentAlerts,
	})
}

func safePercent(num, den int64) float64 {
	if den <= 0 || num <= 0 {
		return 0
	}
	val := (float64(num) / float64(den)) * 100.0
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0
	}
	return math.Round(val*10) / 10
}
