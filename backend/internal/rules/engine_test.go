package rules_test

import (
	"testing"
	"time"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/rules"
)

func intPtr(v int) *int { return &v }

func TestRuleEngineAnomalyCountAndCooldown(t *testing.T) {
	eng := rules.NewEngine()
	roi := rules.ROI{
		ID:      "roi-1",
		Name:    "Workshop Zone",
		ROIType: domain.ROITypeFullFrame,
	}

	eng.SetCameraRules("cam-1", []rules.Rule{
		{
			ID:                 "rule-anomaly",
			CameraID:           "cam-1",
			ROI:                roi,
			AlgorithmVersionID: "ver-1",
			Name:               "No Helmet",
			RuleMode:           domain.RuleModeAnomalyDetect,
			Params: rules.RuleParams{
				Labels:            []string{"no_helmet"},
				MinConfidence:     0.6,
				HitPoint:          domain.HitPointBottomCenter,
				ConsecutiveFrames: 3,
				CooldownSeconds:   30,
				Severity:          domain.SeverityCritical,
			},
			Enabled: true,
		},
		{
			ID:                 "rule-count",
			CameraID:           "cam-1",
			ROI:                roi,
			AlgorithmVersionID: "ver-1",
			Name:               "Max 2 Persons",
			RuleMode:           domain.RuleModeCountThreshold,
			Params: rules.RuleParams{
				Labels:          []string{"person"},
				MinConfidence:   0.5,
				MaxCount:        intPtr(2),
				CooldownSeconds: 30,
				Severity:        domain.SeverityWarning,
			},
			Enabled: true,
		},
	})

	base := time.Unix(1795700000, 0).UTC()

	// Anomaly consecutive_frames=3: frames 1 & 2 should not trigger, frame 3 triggers
	for i := 0; i < 2; i++ {
		out := eng.Evaluate(rules.DetectionFrame{
			CameraID:           "cam-1",
			AlgorithmVersionID: "ver-1",
			Timestamp:          base.Add(time.Duration(i) * time.Second),
			Detections: []rules.Detection{
				{BBox: [4]float64{0.2, 0.2, 0.4, 0.6}, Label: "no_helmet", Confidence: 0.9, IsAbnormal: true},
			},
		})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("frame %d triggered prematurely: %+v", i+1, out.TriggeredAlarms)
		}
	}

	out3 := eng.Evaluate(rules.DetectionFrame{
		CameraID:           "cam-1",
		AlgorithmVersionID: "ver-1",
		Timestamp:          base.Add(2 * time.Second),
		Detections: []rules.Detection{
			{BBox: [4]float64{0.2, 0.2, 0.4, 0.6}, Label: "no_helmet", Confidence: 0.9, IsAbnormal: true},
		},
	})
	if len(out3.TriggeredAlarms) != 1 || out3.TriggeredAlarms[0].RuleID != "rule-anomaly" {
		t.Fatalf("expected 1 anomaly alarm on frame 3, got %+v", out3.TriggeredAlarms)
	}

	// Frame 4..6 within 30s cooldown MUST NOT trigger anomaly again
	for i := 3; i < 6; i++ {
		out := eng.Evaluate(rules.DetectionFrame{
			CameraID:           "cam-1",
			AlgorithmVersionID: "ver-1",
			Timestamp:          base.Add(time.Duration(i) * time.Second),
			Detections: []rules.Detection{
				{BBox: [4]float64{0.2, 0.2, 0.4, 0.6}, Label: "no_helmet", Confidence: 0.9, IsAbnormal: true},
			},
		})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("frame %d triggered during cooldown: %+v", i+1, out.TriggeredAlarms)
		}
	}

	// Count threshold: 3 persons exceeds max_count=2
	outCount := eng.Evaluate(rules.DetectionFrame{
		CameraID:           "cam-1",
		AlgorithmVersionID: "ver-1",
		Timestamp:          base.Add(10 * time.Second),
		Detections: []rules.Detection{
			{BBox: [4]float64{0.1, 0.1, 0.2, 0.5}, Label: "person", Confidence: 0.9},
			{BBox: [4]float64{0.3, 0.1, 0.4, 0.5}, Label: "person", Confidence: 0.8},
			{BBox: [4]float64{0.5, 0.1, 0.6, 0.5}, Label: "person", Confidence: 0.85},
		},
	})
	if outCount.ROICounts["roi-1"] != 3 {
		t.Errorf("ROICounts[roi-1] = %d, want 3", outCount.ROICounts["roi-1"])
	}
	if len(outCount.TriggeredAlarms) != 1 || outCount.TriggeredAlarms[0].RuleID != "rule-count" || outCount.TriggeredAlarms[0].DetectedCount != 3 {
		t.Fatalf("expected 1 count_threshold alarm with count=3, got %+v", outCount.TriggeredAlarms)
	}
}

// Review Focus #1: absence_detect MUST pause/reset timer when heartbeat stops exceeding heartbeat_timeout_seconds,
// preventing false absence alarms when a camera disconnects or Runtime crashes.
func TestRuleEngineAbsencePausesOnHeartbeatTimeout(t *testing.T) {
	eng := rules.NewEngine()
	roi := rules.ROI{
		ID:      "roi-desk",
		Name:    "Duty Station 1",
		ROIType: domain.ROITypeFullFrame,
	}

	rule := rules.Rule{
		ID:                 "rule-absence",
		CameraID:           "cam-duty",
		ROI:                roi,
		AlgorithmVersionID: "ver-person",
		Name:               "Station 1 Absence",
		RuleMode:           domain.RuleModeAbsenceDetect,
		Params: rules.RuleParams{
			Labels:                  []string{"person"},
			MinConfidence:           0.5,
			MinCount:                intPtr(1),
			DurationSeconds:         10,
			HeartbeatTimeoutSeconds: 5,
			CooldownSeconds:         30,
			Severity:                domain.SeverityCritical,
		},
		Enabled: true,
	}
	eng.SetCameraRules("cam-duty", []rules.Rule{rule})

	t0 := time.Unix(1795701000, 0).UTC()

	// Scenario A: continuous empty heartbeat frames every 1s for 10s -> MUST trigger at t=10s
	triggered := false
	for sec := 0; sec <= 10; sec++ {
		out := eng.Evaluate(rules.DetectionFrame{
			CameraID:           "cam-duty",
			AlgorithmVersionID: "ver-person",
			Timestamp:          t0.Add(time.Duration(sec) * time.Second),
			IsHeartbeat:        true,
			Detections:         nil,
		})
		if sec < 10 && len(out.TriggeredAlarms) > 0 {
			t.Fatalf("absence triggered too early at sec=%d: %+v", sec, out.TriggeredAlarms)
		}
		if sec == 10 && len(out.TriggeredAlarms) == 1 && out.TriggeredAlarms[0].RuleID == "rule-absence" {
			triggered = true
		}
	}
	if !triggered {
		t.Fatal("expected absence_detect to trigger after 10s of continuous empty heartbeats")
	}

	// Scenario B (fresh engine): heartbeats arrive for 4s, then stream drops (no heartbeats for 30s!)
	// When a frame finally arrives at t=34s (or even 5s later at t=39s), elapsed across the outage gap MUST NOT count!
	engB := rules.NewEngine()
	engB.SetCameraRules("cam-duty", []rules.Rule{rule})

	for sec := 0; sec <= 4; sec++ {
		out := engB.Evaluate(rules.DetectionFrame{
			CameraID:           "cam-duty",
			AlgorithmVersionID: "ver-person",
			Timestamp:          t0.Add(time.Duration(sec) * time.Second),
			IsHeartbeat:        true,
		})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("unexpected alarm during first 4s: %+v", out.TriggeredAlarms)
		}
	}

	// Outage from t=4s to t=34s (gap=30s > heartbeat_timeout=5s)
	outAfterOutage := engB.Evaluate(rules.DetectionFrame{
		CameraID:           "cam-duty",
		AlgorithmVersionID: "ver-person",
		Timestamp:          t0.Add(34 * time.Second),
		IsHeartbeat:        true,
	})
	if len(outAfterOutage.TriggeredAlarms) != 0 {
		t.Fatalf("absence_detect MUST NOT trigger across a heartbeat timeout outage gap; got %+v", outAfterOutage.TriggeredAlarms)
	}

	// Even 5s after stream recovery (4s pre-outage + 5s post-recovery = 9s < 10s; and timer paused/reset): must NOT fire prematurely
	for sec := 35; sec <= 39; sec++ {
		out := engB.Evaluate(rules.DetectionFrame{
			CameraID:           "cam-duty",
			AlgorithmVersionID: "ver-person",
			Timestamp:          t0.Add(time.Duration(sec) * time.Second),
			IsHeartbeat:        true,
		})
		if len(out.TriggeredAlarms) != 0 {
			t.Fatalf("absence_detect fired prematurely at sec=%d after outage: %+v", sec, out.TriggeredAlarms)
		}
	}
}
