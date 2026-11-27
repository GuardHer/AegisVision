package rules_test

import (
	"testing"
	"time"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/rules"
)

func boolPtr(v bool) *bool { return &v }

func makeSOPTestEngine(rule rules.Rule) *rules.Engine {
	eng := rules.NewEngine()
	eng.SetCameraRules(rule.CameraID, []rules.Rule{rule})
	return eng
}

func detAt(label string, x1, y1, x2, y2 float64) []rules.Detection {
	return []rules.Detection{
		{BBox: [4]float64{x1, y1, x2, y2}, Label: label, Confidence: 0.92},
	}
}

func TestSOPFirstStepCompliantCycleAndCrossROI(t *testing.T) {
	roiMain := rules.ROI{ID: "roi-main", Name: "Workstation", ROIType: domain.ROITypeFullFrame}
	roiScan := rules.ROI{
		ID:      "roi-scan",
		Name:    "ScannerZone",
		ROIType: domain.ROITypePolygon,
		Points:  []rules.Point{{X: 0.5, Y: 0.0}, {X: 1.0, Y: 0.0}, {X: 1.0, Y: 1.0}, {X: 0.5, Y: 1.0}},
	}

	eng := makeSOPTestEngine(rules.Rule{
		ID:                 "sop-1",
		CameraID:           "cam-1",
		ROI:                roiMain,
		CameraROIs:         map[string]rules.ROI{roiMain.ID: roiMain, roiScan.ID: roiScan},
		AlgorithmVersionID: "ver-1",
		Name:               "ESD-Scan-SOP",
		RuleMode:           domain.RuleModeSOPSequence,
		Params: rules.RuleParams{
			TriggerMode:             "first_step",
			HeartbeatTimeoutSeconds: 5,
			CycleTimeoutSeconds:     60,
			CooldownSeconds:         10,
			Severity:                domain.SeverityWarning,
			ViolationTypes:          []string{"missing_step", "out_of_order", "step_timeout"},
			Steps: []rules.SOPStepSpec{
				{StepID: "s1", Name: "Wear ESD", Labels: []string{"wear_esd"}, MinConfidence: 0.6, MinDwellFrames: 2, MaxWaitSeconds: 15, Required: boolPtr(true)},
				{StepID: "s2", Name: "Scan Barcode", Labels: []string{"scan"}, ROIID: "roi-scan", MinConfidence: 0.6, HitPoint: domain.HitPointCenter, MinDwellFrames: 1, MaxWaitSeconds: 15, Required: boolPtr(true)},
			},
		},
		Enabled: true,
	})

	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

	// Frame 1: dwell 1/2 on s1 -> still idle
	out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", AlgorithmVersionID: "ver-1", Timestamp: t0, Detections: detAt("wear_esd", 0.1, 0.2, 0.3, 0.4)})
	if len(out.TriggeredAlarms) != 0 || len(out.CompletedSOPCycles) != 0 || len(out.SOPProgress) != 1 || out.SOPProgress[0].State != "idle" {
		t.Fatalf("expected idle after 1st dwell frame, got %+v", out)
	}

	// Frame 2: dwell 2/2 on s1 -> s1 completed, cycle in_progress waiting for s2
	out = eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", AlgorithmVersionID: "ver-1", Timestamp: t0.Add(time.Second), Detections: detAt("wear_esd", 0.1, 0.2, 0.3, 0.4)})
	if len(out.TriggeredAlarms) != 0 || len(out.SOPProgress) != 1 || out.SOPProgress[0].State != "in_progress" || out.SOPProgress[0].CurrentStepIndex != 1 {
		t.Fatalf("expected in_progress step=1 after s1 dwell, got %+v", out.SOPProgress)
	}

	// Frame 3: scan detection outside roi-scan (left half x=0.1..0.3) -> ignored, still in_progress
	out = eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", AlgorithmVersionID: "ver-1", Timestamp: t0.Add(2 * time.Second), Detections: detAt("scan", 0.1, 0.2, 0.3, 0.4)})
	if len(out.CompletedSOPCycles) != 0 || len(out.TriggeredAlarms) != 0 {
		t.Fatalf("scan outside roi-scan must not complete s2: %+v", out)
	}

	// Frame 4: scan detection inside roi-scan (right half x=0.6..0.8) -> compliant cycle completed!
	out = eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", AlgorithmVersionID: "ver-1", Timestamp: t0.Add(3 * time.Second), Detections: detAt("scan", 0.6, 0.2, 0.8, 0.4)})
	if len(out.TriggeredAlarms) != 0 || len(out.CompletedSOPCycles) != 1 {
		t.Fatalf("expected 1 compliant SOP cycle and 0 alarms, got alarms=%d cycles=%+v", len(out.TriggeredAlarms), out.CompletedSOPCycles)
	}
	if out.CompletedSOPCycles[0].DurationMs != 2000 {
		t.Fatalf("expected cycle duration 2000ms, got %d", out.CompletedSOPCycles[0].DurationMs)
	}
}

func TestSOPOptionalStepSilentlySkippedAndNeverTimesOut(t *testing.T) {
	eng := makeSOPTestEngine(rules.Rule{
		ID:       "sop-opt",
		CameraID: "cam-1",
		ROI:      rules.ROI{ID: "roi-1", Name: "Station", ROIType: domain.ROITypeFullFrame},
		Name:     "Optional-Middle",
		RuleMode: domain.RuleModeSOPSequence,
		Params: rules.RuleParams{
			TriggerMode:             "first_step",
			HeartbeatTimeoutSeconds: 5,
			CycleTimeoutSeconds:     120,
			ViolationTypes:          []string{"missing_step", "out_of_order", "step_timeout"},
			Steps: []rules.SOPStepSpec{
				{StepID: "s1", Name: "Step1", Labels: []string{"a"}, MinConfidence: 0.5, Required: boolPtr(true)},
				{StepID: "s2", Name: "OptionalClean", Labels: []string{"b"}, MinConfidence: 0.5, MaxWaitSeconds: 2, Required: boolPtr(false)},
				{StepID: "s3", Name: "Step3", Labels: []string{"c"}, MinConfidence: 0.5, MaxWaitSeconds: 30, Required: boolPtr(true)},
			},
		},
		Enabled: true,
	})

	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0, Detections: detAt("a", 0.2, 0.2, 0.4, 0.4)})

	// Wait 4s (exceeding optional s2 max_wait_seconds=2) with heartbeats -> must NOT timeout!
	for sec := 1; sec <= 4; sec++ {
		out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(time.Duration(sec) * time.Second), IsHeartbeat: true})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("optional step s2 must never trigger step_timeout at t+%ds: %+v", sec, out.TriggeredAlarms)
		}
	}

	// Jump straight to s3 -> s2 silently skipped, cycle completes compliant with zero alarms!
	out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(5 * time.Second), Detections: detAt("c", 0.2, 0.2, 0.4, 0.4)})
	if len(out.TriggeredAlarms) != 0 || len(out.CompletedSOPCycles) != 1 {
		t.Fatalf("expected optional step to skip silently and complete cycle, got alarms=%+v cycles=%+v", out.TriggeredAlarms, out.CompletedSOPCycles)
	}
}

func TestSOPMissingStepOutOfOrderAndHeartbeatGapPause(t *testing.T) {
	eng := makeSOPTestEngine(rules.Rule{
		ID:       "sop-viol",
		CameraID: "cam-1",
		ROI:      rules.ROI{ID: "roi-1", Name: "Station", ROIType: domain.ROITypeFullFrame},
		Name:     "Strict-3-Step",
		RuleMode: domain.RuleModeSOPSequence,
		Params: rules.RuleParams{
			TriggerMode:             "first_step",
			HeartbeatTimeoutSeconds: 5,
			CycleTimeoutSeconds:     60,
			CooldownSeconds:         0,
			Severity:                domain.SeverityCritical,
			ViolationTypes:          []string{"missing_step", "out_of_order", "step_timeout"},
			Steps: []rules.SOPStepSpec{
				{StepID: "s1", Name: "Step1", Labels: []string{"s1"}, MinConfidence: 0.5, MaxWaitSeconds: 4, Required: boolPtr(true)},
				{StepID: "s2", Name: "Step2", Labels: []string{"s2"}, MinConfidence: 0.5, MaxWaitSeconds: 4, Required: boolPtr(true)},
				{StepID: "s3", Name: "Step3", Labels: []string{"s3"}, MinConfidence: 0.5, MaxWaitSeconds: 4, Required: boolPtr(true)},
			},
		},
		Enabled: true,
	})

	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

	// 1. Out-of-order from idle (hit s2 skipping required s1) -> triggers violation with SOPTrace
	out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0, Detections: detAt("s2", 0.2, 0.2, 0.4, 0.4)})
	if len(out.TriggeredAlarms) != 1 || out.TriggeredAlarms[0].SOPTrace == nil {
		t.Fatalf("expected 1 violation alarm with SOPTrace when skipping s1, got %+v", out.TriggeredAlarms)
	}
	if out.TriggeredAlarms[0].SOPTrace.ViolatedStepID != "s1" {
		t.Fatalf("expected violated_step_id=s1, got %+v", out.TriggeredAlarms[0].SOPTrace)
	}

	// 2. Start s1, then simulate 12s stream drop (> heartbeat_timeout_seconds=5) -> must NOT fire step_timeout!
	t1 := t0.Add(10 * time.Second)
	eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t1, Detections: detAt("s1", 0.2, 0.2, 0.4, 0.4)})
	outGap := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t1.Add(12 * time.Second), IsHeartbeat: true})
	if len(outGap.TriggeredAlarms) != 0 {
		t.Fatalf("stream drop gap exceeding heartbeat timeout must NOT trigger step_timeout: %+v", outGap.TriggeredAlarms)
	}

	// 3. Resume normal 1s heartbeats -> after 5s (> max_wait_seconds=4 on s2), fires step_timeout!
	var timedOut *rules.TriggeredAlarm
	for sec := 13; sec <= 18; sec++ {
		o := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t1.Add(time.Duration(sec) * time.Second), IsHeartbeat: true})
		if len(o.TriggeredAlarms) > 0 {
			timedOut = &o.TriggeredAlarms[0]
			break
		}
	}
	if timedOut == nil || timedOut.ViolationType != "step_timeout" || timedOut.SOPTrace == nil || timedOut.SOPTrace.ViolatedStepID != "s2" {
		t.Fatalf("expected step_timeout on s2 after normal heartbeats, got %+v", timedOut)
	}
}

func TestSOPZoneEntryRearmProtection(t *testing.T) {
	eng := makeSOPTestEngine(rules.Rule{
		ID:       "sop-entry",
		CameraID: "cam-1",
		ROI:      rules.ROI{ID: "roi-1", Name: "Station", ROIType: domain.ROITypeFullFrame},
		Name:     "Entry-SOP",
		RuleMode: domain.RuleModeSOPSequence,
		Params: rules.RuleParams{
			TriggerMode:             "zone_entry",
			EntryLabels:             []string{"person"},
			RearmCooldownSeconds:    30,
			HeartbeatTimeoutSeconds: 5,
			CooldownSeconds:         0,
			ViolationTypes:          []string{"step_timeout"},
			Steps: []rules.SOPStepSpec{
				{StepID: "s1", Name: "TouchPanel", Labels: []string{"touch"}, MinConfidence: 0.5, MaxWaitSeconds: 3, Required: boolPtr(true)},
			},
		},
		Enabled: true,
	})

	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	personDet := detAt("person", 0.2, 0.2, 0.4, 0.8)

	// Person enters and stays without touching panel -> timeout at t+4s
	var firstAlarm bool
	for sec := 0; sec <= 4; sec++ {
		out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(time.Duration(sec) * time.Second), Detections: personDet})
		if len(out.TriggeredAlarms) == 1 {
			firstAlarm = true
		}
	}
	if !firstAlarm {
		t.Fatal("expected initial zone_entry timeout alarm")
	}

	// Review Focus #3: Person stays stationary for 10s (< rearm_cooldown_seconds=30) -> MUST stay in rearm & fire 0 alarms!
	for sec := 5; sec <= 15; sec++ {
		out := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(time.Duration(sec) * time.Second), Detections: personDet})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("stationary person in rearm guard must not re-trigger alarm at t+%ds", sec)
		}
		if out.SOPProgress[0].State != "rearm" {
			t.Fatalf("expected rearm state while stationary, got %s", out.SOPProgress[0].State)
		}
	}

	// Person exits for 1 frame -> returns to idle; re-enters -> starts new cycle cleanly!
	outExit := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(16 * time.Second), IsHeartbeat: true})
	if outExit.SOPProgress[0].State != "idle" {
		t.Fatalf("expected idle after person exits ROI, got %s", outExit.SOPProgress[0].State)
	}
	outReenter := eng.Evaluate(rules.DetectionFrame{CameraID: "cam-1", Timestamp: t0.Add(17 * time.Second), Detections: personDet})
	if outReenter.SOPProgress[0].State != "in_progress" {
		t.Fatalf("expected in_progress upon person re-entry, got %s", outReenter.SOPProgress[0].State)
	}
}
