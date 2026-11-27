package rules_test

import (
	"testing"
	"time"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/rules"
)

func mustTimeInLoc(t *testing.T, layout, val, tz string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("LoadLocation(%s): %v", tz, err)
	}
	parsed, err := time.ParseInLocation(layout, val, loc)
	if err != nil {
		t.Fatalf("ParseInLocation(%s): %v", val, err)
	}
	return parsed
}

func TestArmingScheduleDayNightAndHolidayExceptions(t *testing.T) {
	// 1. Daytime weekday schedule: Mon-Fri 08:00-20:00 Asia/Shanghai
	daySched, err := rules.ParseArmingSchedule(
		"sched-day",
		"Day Shift",
		"Asia/Shanghai",
		true,
		[]byte(`[{"days":[1,2,3,4,5],"start":"08:00","end":"20:00"}]`),
		[]byte(`["2026-10-01"]`),
		[]byte(`[{"date":"2026-10-03","start":"09:00","end":"17:00"},{"date":"2026-10-04"}]`),
	)
	if err != nil {
		t.Fatalf("ParseArmingSchedule day: %v", err)
	}

	// Monday 2026-09-28 10:00 -> armed
	if !daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-09-28 10:00", "Asia/Shanghai")) {
		t.Fatal("expected armed on Monday 10:00")
	}
	// Monday 2026-09-28 21:00 -> disarmed
	if daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-09-28 21:00", "Asia/Shanghai")) {
		t.Fatal("expected disarmed on Monday 21:00")
	}
	// Sunday 2026-09-27 10:00 -> disarmed
	if daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-09-27 10:00", "Asia/Shanghai")) {
		t.Fatal("expected disarmed on Sunday 10:00")
	}
	// Thursday 2026-10-01 10:00 -> excluded holiday -> disarmed
	if daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-01 10:00", "Asia/Shanghai")) {
		t.Fatal("expected disarmed on excluded holiday 2026-10-01")
	}
	// Saturday 2026-10-03 10:00 -> include_date 09:00-17:00 -> armed; 18:00 -> disarmed
	if !daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-03 10:00", "Asia/Shanghai")) {
		t.Fatal("expected armed during include_date window 2026-10-03 10:00")
	}
	if daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-03 18:00", "Asia/Shanghai")) {
		t.Fatal("expected disarmed after include_date window 2026-10-03 18:00")
	}
	// Sunday 2026-10-04 23:30 -> full-day include_date (omitted start/end) -> armed
	if !daySched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-04 23:30", "Asia/Shanghai")) {
		t.Fatal("expected armed on full-day include_date 2026-10-04 23:30")
	}

	// 2. Overnight night shift: Mon-Fri 20:00-08:00, holiday on Thu 2026-10-01
	nightSched, err := rules.ParseArmingSchedule(
		"sched-night",
		"Night Shift",
		"Asia/Shanghai",
		true,
		[]byte(`[{"days":[1,2,3,4,5],"start":"20:00","end":"08:00"}]`),
		[]byte(`["2026-10-01"]`),
		[]byte(`[{"date":"2026-10-01"}]`), // exclude_dates must override include_dates
	)
	if err != nil {
		t.Fatalf("ParseArmingSchedule night: %v", err)
	}

	// Wed 2026-09-30 23:30 -> armed (same evening)
	if !nightSched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-09-30 23:30", "Asia/Shanghai")) {
		t.Fatal("expected armed on Wed night 23:30")
	}
	// Review Focus #1: Thu 2026-10-01 00:05 -> excluded calendar day immediately disarms overnight shift!
	if nightSched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-01 00:05", "Asia/Shanghai")) {
		t.Fatal("expected excluded holiday 2026-10-01 00:05 to immediately disarm previous night's overnight shift")
	}
	// Sat 2026-10-03 02:00 -> post-midnight tail of Fri 2026-10-02 night shift -> armed!
	if !nightSched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-03 02:00", "Asia/Shanghai")) {
		t.Fatal("expected armed on Saturday 02:00 from Friday night shift")
	}
	// Sat 2026-10-03 09:00 -> past 08:00 -> disarmed
	if nightSched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-10-03 09:00", "Asia/Shanghai")) {
		t.Fatal("expected disarmed on Saturday 09:00")
	}

	// Disabled schedule template always returns false
	disabledSched, err := rules.ParseArmingSchedule(
		"sched-off", "Paused", "Asia/Shanghai", false,
		[]byte(`[{"days":[1,2,3,4,5,6,7],"start":"00:00","end":"24:00"}]`),
		nil, nil,
	)
	if err != nil {
		t.Fatalf("ParseArmingSchedule disabled: %v", err)
	}
	if disabledSched.IsArmed(mustTimeInLoc(t, "2006-01-02 15:04", "2026-09-28 10:00", "Asia/Shanghai")) {
		t.Fatal("disabled schedule must not be armed")
	}
}

func TestArmingScheduleDisarmedResetsTransientStateAndKeepsLiveCounts(t *testing.T) {
	sched, err := rules.ParseArmingSchedule(
		"sched-day", "Day", "Asia/Shanghai", true,
		[]byte(`[{"days":[1],"start":"08:00","end":"20:00"}]`),
		nil, nil,
	)
	if err != nil {
		t.Fatalf("ParseArmingSchedule: %v", err)
	}

	eng := rules.NewEngine()
	eng.SetCameraRules("cam-1", []rules.Rule{
		{
			ID:       "r-1",
			CameraID: "cam-1",
			ROI:      rules.ROI{ID: "roi-1", Name: "ZoneA", ROIType: domain.ROITypeFullFrame},
			Name:     "NoHelmet",
			RuleMode: domain.RuleModeAnomalyDetect,
			Schedule: sched,
			Params: rules.RuleParams{
				Labels:            []string{"no_helmet"},
				MinConfidence:     0.5,
				ConsecutiveFrames: 2,
				Severity:          domain.SeverityCritical,
			},
			Enabled: true,
		},
	})

	det := []rules.Detection{{BBox: [4]float64{0.2, 0.2, 0.4, 0.4}, Label: "no_helmet", Confidence: 0.9, IsAbnormal: true}}

	// 1. Disarmed Monday 07:59:58 & 07:59:59: must NOT trigger alarm, MUST still populate ROICounts
	t1 := mustTimeInLoc(t, "2006-01-02 15:04:05", "2026-09-28 07:59:58", "Asia/Shanghai")
	out1 := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t1, Detections: det})
	if len(out1.TriggeredAlarms) != 0 || out1.ROICounts["roi-1"] != 1 {
		t.Fatalf("expected 0 alarms and live roi count=1 during disarmed period, got alarms=%d count=%d", len(out1.TriggeredAlarms), out1.ROICounts["roi-1"])
	}
	out2 := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t1.Add(time.Second), Detections: det})
	if len(out2.TriggeredAlarms) != 0 {
		t.Fatalf("expected 0 alarms on 2nd disarmed frame, got %d", len(out2.TriggeredAlarms))
	}

	// 2. Enter armed window at 08:00:00 -> 1st armed frame must NOT trigger (requires 2 consecutive armed frames)
	tArmed := mustTimeInLoc(t, "2006-01-02 15:04:05", "2026-09-28 08:00:00", "Asia/Shanghai")
	out3 := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: tArmed, Detections: det})
	if len(out3.TriggeredAlarms) != 0 {
		t.Fatalf("expected stale disarmed hits to be cleared; got premature alarm on 1st armed frame")
	}
	// 3. 2nd armed frame at 08:00:01 -> triggers cleanly!
	out4 := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: tArmed.Add(time.Second), Detections: det})
	if len(out4.TriggeredAlarms) != 1 {
		t.Fatalf("expected 1 alarm on 2nd armed frame, got %d", len(out4.TriggeredAlarms))
	}
}
