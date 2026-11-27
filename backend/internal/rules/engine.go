package rules

import (
	"fmt"
	"sync"
	"time"

	"aegisvision/backend/internal/domain"
)

// Detection is a single model prediction in normalized [0.0, 1.0] coordinates.
type Detection struct {
	BBox       [4]float64        `json:"bbox"`
	Label      string            `json:"label"`
	Confidence float64           `json:"confidence"`
	TrackID    string            `json:"track_id,omitempty"`
	IsAbnormal bool              `json:"is_abnormal"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// DetectionFrame is the input frame evaluated by the in-memory rule engine.
type DetectionFrame struct {
	TaskID             string
	CameraID           string
	AlgorithmVersionID string
	Timestamp          time.Time
	IsHeartbeat        bool
	Detections         []Detection
}

// RuleParams corresponds to roi_algorithm_rules.params JSONB schema.
type RuleParams struct {
	Labels                  []string `json:"labels"`
	RequireIsAbnormal       bool     `json:"require_is_abnormal"`
	MinConfidence           float64  `json:"min_confidence"`
	HitPoint                string   `json:"hit_point"`
	MinCount                *int     `json:"min_count,omitempty"`
	MaxCount                *int     `json:"max_count,omitempty"`
	ConsecutiveFrames       int      `json:"consecutive_frames"`
	DurationSeconds         int      `json:"duration_seconds"`
	CooldownSeconds         int      `json:"cooldown_seconds"`
	HeartbeatTimeoutSeconds int      `json:"heartbeat_timeout_seconds"`
	Severity                string   `json:"severity"`
}

// Rule is a configured ROI × AlgorithmVersion evaluation rule.
type Rule struct {
	ID                 string
	CameraID           string
	ROI                ROI
	AlgorithmVersionID string
	AlgorithmKey       string
	Name               string
	RuleMode           string
	Params             RuleParams
	RecordVideo        bool
	VideoPreSeconds    int
	VideoPostSeconds   int
	Enabled            bool
}

// TriggeredAlarm represents a single-shot alarm produced by rule evaluation.
type TriggeredAlarm struct {
	RuleID             string
	CameraID           string
	ROI                ROI
	AlgorithmVersionID string
	AlgorithmKey       string
	RuleMode           string
	Title              string
	Severity           string
	TriggeredAt        time.Time
	DetectedCount      int
	MatchedDetections  []Detection
	RecordVideo        bool
	VideoPreSeconds    int
	VideoPostSeconds   int
}

// EvalOutput bundles per-ROI live counts and any alarms triggered by a frame.
type EvalOutput struct {
	ROICounts       map[string]int
	TriggeredAlarms []TriggeredAlarm
}

type ruleRuntimeState struct {
	consecutiveHits int
	conditionSince  time.Time
	lastHeartbeatAt time.Time
	absentElapsed   time.Duration
	lastAlarmAt     time.Time
}

type cameraBucket struct {
	mu     sync.Mutex
	rules  []Rule
	states map[string]*ruleRuntimeState
}

// Engine evaluates detection frames against ROI rules with per-camera sharded locking (zero IO).
type Engine struct {
	buckets sync.Map // map[cameraID string]*cameraBucket
}

// NewEngine creates an empty in-memory rule engine.
func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) getOrCreateBucket(cameraID string) *cameraBucket {
	if v, ok := e.buckets.Load(cameraID); ok {
		return v.(*cameraBucket)
	}
	nb := &cameraBucket{
		states: make(map[string]*ruleRuntimeState),
	}
	actual, _ := e.buckets.LoadOrStore(cameraID, nb)
	return actual.(*cameraBucket)
}

// SetCameraRules replaces the active rule list for cameraID while preserving state for unchanged rules.
func (e *Engine) SetCameraRules(cameraID string, ruleList []Rule) {
	b := e.getOrCreateBucket(cameraID)
	b.mu.Lock()
	defer b.mu.Unlock()

	copied := make([]Rule, len(ruleList))
	copy(copied, ruleList)
	b.rules = copied

	valid := make(map[string]struct{}, len(copied))
	for _, r := range copied {
		valid[r.ID] = struct{}{}
	}
	for id := range b.states {
		if _, ok := valid[id]; !ok {
			delete(b.states, id)
		}
	}
}

// Evaluate runs all matching rules for frame.CameraID and frame.AlgorithmVersionID.
func (e *Engine) Evaluate(frame DetectionFrame) EvalOutput {
	out := EvalOutput{
		ROICounts: make(map[string]int),
	}
	if frame.CameraID == "" {
		return out
	}

	ts := frame.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	b := e.getOrCreateBucket(frame.CameraID)
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, rule := range b.rules {
		if !rule.Enabled {
			continue
		}
		if rule.AlgorithmVersionID != "" && frame.AlgorithmVersionID != "" && rule.AlgorithmVersionID != frame.AlgorithmVersionID {
			continue
		}

		matched := filterMatchingDetections(frame.Detections, rule)
		count := len(matched)
		if prev, exists := out.ROICounts[rule.ROI.ID]; !exists || count > prev {
			out.ROICounts[rule.ROI.ID] = count
		}

		st := b.states[rule.ID]
		if st == nil {
			st = &ruleRuntimeState{}
			b.states[rule.ID] = st
		}

		if alarm, ok := evaluateSingleRule(rule, st, matched, count, ts); ok {
			out.TriggeredAlarms = append(out.TriggeredAlarms, alarm)
		}
	}

	return out
}

func filterMatchingDetections(dets []Detection, rule Rule) []Detection {
	if len(dets) == 0 {
		return nil
	}
	labelSet := make(map[string]struct{}, len(rule.Params.Labels))
	for _, l := range rule.Params.Labels {
		labelSet[l] = struct{}{}
	}
	hitPoint := rule.Params.HitPoint
	if hitPoint == "" {
		hitPoint = domain.HitPointBottomCenter
	}

	var matched []Detection
	for _, d := range dets {
		if d.Confidence < rule.Params.MinConfidence {
			continue
		}
		if rule.Params.RequireIsAbnormal && !d.IsAbnormal {
			continue
		}
		if len(labelSet) > 0 {
			if _, ok := labelSet[d.Label]; !ok {
				continue
			}
		}
		if !DetectionHitsROI(d.BBox, rule.ROI, hitPoint) {
			continue
		}
		matched = append(matched, d)
	}
	return matched
}

func evaluateSingleRule(rule Rule, st *ruleRuntimeState, matched []Detection, count int, ts time.Time) (TriggeredAlarm, bool) {
	cooldown := time.Duration(rule.Params.CooldownSeconds) * time.Second
	inCooldown := !st.lastAlarmAt.IsZero() && cooldown > 0 && ts.Sub(st.lastAlarmAt) < cooldown

	severity := rule.Params.Severity
	if severity == "" {
		severity = domain.SeverityWarning
	}

	switch rule.RuleMode {
	case domain.RuleModeAnomalyDetect:
		if count > 0 {
			st.consecutiveHits++
			if st.conditionSince.IsZero() {
				st.conditionSince = ts
			}
			minFrames := rule.Params.ConsecutiveFrames
			if minFrames <= 0 {
				minFrames = 1
			}
			minDur := time.Duration(rule.Params.DurationSeconds) * time.Second
			if st.consecutiveHits >= minFrames && ts.Sub(st.conditionSince) >= minDur && !inCooldown {
				st.lastAlarmAt = ts
				st.consecutiveHits = 0
				st.conditionSince = time.Time{}
				return buildTriggeredAlarm(rule, severity, ts, count, matched, fmt.Sprintf("%s: %s", rule.ROI.Name, rule.Name)), true
			}
		} else {
			st.consecutiveHits = 0
			st.conditionSince = time.Time{}
		}

	case domain.RuleModeCountThreshold:
		violated := false
		if rule.Params.MinCount != nil && count < *rule.Params.MinCount {
			violated = true
		}
		if rule.Params.MaxCount != nil && count > *rule.Params.MaxCount {
			violated = true
		}
		if violated {
			if st.conditionSince.IsZero() {
				st.conditionSince = ts
			}
			minDur := time.Duration(rule.Params.DurationSeconds) * time.Second
			if ts.Sub(st.conditionSince) >= minDur && !inCooldown {
				st.lastAlarmAt = ts
				st.conditionSince = time.Time{}
				return buildTriggeredAlarm(rule, severity, ts, count, matched, fmt.Sprintf("%s: %s (count=%d)", rule.ROI.Name, rule.Name, count)), true
			}
		} else {
			st.conditionSince = time.Time{}
		}

	case domain.RuleModeAbsenceDetect:
		minCount := 1
		if rule.Params.MinCount != nil && *rule.Params.MinCount > 0 {
			minCount = *rule.Params.MinCount
		}
		hbTimeoutSec := rule.Params.HeartbeatTimeoutSeconds
		if hbTimeoutSec <= 0 {
			hbTimeoutSec = 5
		}
		hbTimeout := time.Duration(hbTimeoutSec) * time.Second
		duration := time.Duration(rule.Params.DurationSeconds) * time.Second

		if count >= minCount {
			// Personnel present: reset absence timer and refresh heartbeat anchor.
			st.absentElapsed = 0
			st.lastHeartbeatAt = ts
			return TriggeredAlarm{}, false
		}

		// Absent on this frame (including empty heartbeat frames):
		// ONLY advance timer when previous valid frame arrived within heartbeat_timeout_seconds.
		if st.lastHeartbeatAt.IsZero() {
			st.lastHeartbeatAt = ts
			return TriggeredAlarm{}, false
		}

		gap := ts.Sub(st.lastHeartbeatAt)
		st.lastHeartbeatAt = ts
		if gap < 0 || gap > hbTimeout {
			// Stream drop or Runtime crash gap exceeded heartbeat timeout: pause/reset to prevent false alarm.
			st.absentElapsed = 0
			return TriggeredAlarm{}, false
		}

		st.absentElapsed += gap
		if st.absentElapsed >= duration && !inCooldown {
			st.lastAlarmAt = ts
			st.absentElapsed = 0
			return buildTriggeredAlarm(rule, severity, ts, count, nil, fmt.Sprintf("%s: %s (%ds)", rule.ROI.Name, rule.Name, rule.Params.DurationSeconds)), true
		}
	}

	return TriggeredAlarm{}, false
}

func buildTriggeredAlarm(rule Rule, severity string, ts time.Time, count int, matched []Detection, title string) TriggeredAlarm {
	dets := make([]Detection, len(matched))
	copy(dets, matched)
	return TriggeredAlarm{
		RuleID:             rule.ID,
		CameraID:           rule.CameraID,
		ROI:                rule.ROI,
		AlgorithmVersionID: rule.AlgorithmVersionID,
		AlgorithmKey:       rule.AlgorithmKey,
		RuleMode:           rule.RuleMode,
		Title:              title,
		Severity:           severity,
		TriggeredAt:        ts,
		DetectedCount:      count,
		MatchedDetections:  dets,
		RecordVideo:        rule.RecordVideo,
		VideoPreSeconds:    rule.VideoPreSeconds,
		VideoPostSeconds:   rule.VideoPostSeconds,
	}
}
