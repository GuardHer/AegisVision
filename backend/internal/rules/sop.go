package rules

import (
	"fmt"
	"time"

	"aegisvision/backend/internal/domain"
)

// SOPStepSpec defines a single ordered step inside a sop_sequence rule.
type SOPStepSpec struct {
	StepID         string   `json:"step_id"`
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	ROIID          string   `json:"roi_id,omitempty"`
	MinConfidence  float64  `json:"min_confidence"`
	HitPoint       string   `json:"hit_point,omitempty"`
	MinDwellFrames int      `json:"min_dwell_frames,omitempty"`
	MaxWaitSeconds int      `json:"max_wait_seconds,omitempty"`
	Required       *bool    `json:"required,omitempty"`
}

// IsRequired returns true unless the step explicitly sets required=false.
func (s SOPStepSpec) IsRequired() bool {
	return s.Required == nil || *s.Required
}

// SOPStepTraceItem captures one step's status for real-time progress or alarm violation traces.
type SOPStepTraceItem struct {
	StepID      string     `json:"step_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"` // pending | active | completed | skipped | violated
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ElapsedMs   int64      `json:"elapsed_ms,omitempty"`
}

// SOPTrace is persisted in alarms.sop_trace when a sop_sequence rule triggers a violation alarm.
type SOPTrace struct {
	TriggerMode      string             `json:"trigger_mode"`
	ViolationType    string             `json:"violation_type"`
	ViolatedStepID   string             `json:"violated_step_id,omitempty"`
	ViolatedStepName string             `json:"violated_step_name,omitempty"`
	StartedAt        time.Time          `json:"started_at"`
	EndedAt          time.Time          `json:"ended_at"`
	DurationMs       int64              `json:"duration_ms"`
	Steps            []SOPStepTraceItem `json:"steps"`
}

// SOPRuleProgress is broadcast transiently inside frame_detections for live monitor wall step bars.
type SOPRuleProgress struct {
	RuleID           string             `json:"rule_id"`
	RuleName         string             `json:"rule_name"`
	State            string             `json:"state"` // idle | in_progress | rearm
	CurrentStepIndex int                `json:"current_step_index"`
	Steps            []SOPStepTraceItem `json:"steps"`
}

// CompletedSOPCycle records a compliant SOP cycle completion to persist into sop_cycles.
type CompletedSOPCycle struct {
	RuleID             string
	CameraID           string
	AlgorithmVersionID string
	StartedAt          time.Time
	EndedAt            time.Time
	DurationMs         int64
}

type sopRuntimeState struct {
	state           string // idle | in_progress | rearm
	cycleStartedAt  time.Time
	lastHeartbeatAt time.Time
	stepElapsed     time.Duration
	cycleElapsed    time.Duration
	currentStepIdx  int
	stepStatuses    []string
	stepCompletedAt []*time.Time
	stepElapsedMs   []int64
	dwellCounts     map[int]int
	rearmSince      time.Time
}

func newSOPRuntimeState(stepCount int) *sopRuntimeState {
	st := &sopRuntimeState{
		state:       "idle",
		dwellCounts: make(map[int]int, stepCount),
	}
	st.resetSteps(stepCount)
	return st
}

func (s *sopRuntimeState) resetSteps(stepCount int) {
	s.stepStatuses = make([]string, stepCount)
	s.stepCompletedAt = make([]*time.Time, stepCount)
	s.stepElapsedMs = make([]int64, stepCount)
	for i := 0; i < stepCount; i++ {
		s.stepStatuses[i] = "pending"
	}
	s.currentStepIdx = 0
	s.stepElapsed = 0
	s.cycleElapsed = 0
	s.cycleStartedAt = time.Time{}
	s.lastHeartbeatAt = time.Time{}
	for k := range s.dwellCounts {
		delete(s.dwellCounts, k)
	}
}

func (s *sopRuntimeState) resetToIdle(stepCount int) {
	s.state = "idle"
	s.rearmSince = time.Time{}
	s.resetSteps(stepCount)
}

func (s *sopRuntimeState) resetToRearm(stepCount int, ts time.Time) {
	s.state = "rearm"
	s.rearmSince = ts
	s.resetSteps(stepCount)
}

func evaluateSOPRule(rule Rule, st *ruleRuntimeState, frame DetectionFrame, ts time.Time) (*TriggeredAlarm, *CompletedSOPCycle, SOPRuleProgress) {
	steps := rule.Params.Steps
	if len(steps) == 0 {
		return nil, nil, SOPRuleProgress{
			RuleID:   rule.ID,
			RuleName: rule.Name,
			State:    "idle",
			Steps:    []SOPStepTraceItem{},
		}
	}

	if st.sop == nil || len(st.sop.stepStatuses) != len(steps) {
		st.sop = newSOPRuntimeState(len(steps))
	}
	ss := st.sop

	triggerMode := rule.Params.TriggerMode
	if triggerMode == "" {
		triggerMode = "first_step"
	}
	hbTimeoutSec := rule.Params.HeartbeatTimeoutSeconds
	if hbTimeoutSec <= 0 {
		hbTimeoutSec = 5
	}
	hbTimeout := time.Duration(hbTimeoutSec) * time.Second

	rearmCooldownSec := rule.Params.RearmCooldownSeconds
	if rearmCooldownSec <= 0 {
		rearmCooldownSec = 30
	}
	rearmCooldown := time.Duration(rearmCooldownSec) * time.Second

	cooldown := time.Duration(rule.Params.CooldownSeconds) * time.Second
	inCooldown := !st.lastAlarmAt.IsZero() && cooldown > 0 && ts.Sub(st.lastAlarmAt) < cooldown

	severity := rule.Params.Severity
	if severity == "" {
		severity = domain.SeverityWarning
	}

	violationEnabled := buildViolationSet(rule.Params.ViolationTypes)

	// Detect zone entry presence for zone_entry trigger mode.
	entryPresent := false
	if triggerMode == "zone_entry" {
		entryLabels := rule.Params.EntryLabels
		if len(entryLabels) == 0 {
			entryLabels = []string{"person"}
		}
		entryPresent = len(filterDetectionsForSpec(frame.Detections, rule.ROI, entryLabels, rule.Params.MinConfidence, rule.Params.HitPoint)) > 0
	}

	// Handle re-arm guard before evaluating new cycles.
	if ss.state == "rearm" {
		if !entryPresent {
			ss.resetToIdle(len(steps))
		} else if rearmCooldown > 0 && !ss.rearmSince.IsZero() && ts.Sub(ss.rearmSince) >= rearmCooldown {
			ss.resetToIdle(len(steps))
		} else {
			return nil, nil, buildSOPProgress(rule, ss)
		}
	}

	// Evaluate per-step matches & dwell counters.
	stepMatched := make([][]Detection, len(steps))
	stepConfirmed := make([]bool, len(steps))
	for i, sp := range steps {
		targetROI := rule.ROI
		if sp.ROIID != "" && rule.CameraROIs != nil {
			if customROI, ok := rule.CameraROIs[sp.ROIID]; ok {
				targetROI = customROI
			}
		}
		hp := sp.HitPoint
		if hp == "" {
			hp = domain.HitPointCenter
		}
		matched := filterDetectionsForSpec(frame.Detections, targetROI, sp.Labels, sp.MinConfidence, hp)
		stepMatched[i] = matched
		if len(matched) > 0 {
			ss.dwellCounts[i]++
		} else {
			ss.dwellCounts[i] = 0
		}
		need := sp.MinDwellFrames
		if need <= 0 {
			need = 1
		}
		if ss.dwellCounts[i] >= need {
			stepConfirmed[i] = true
		}
	}

	finishReset := func() {
		if triggerMode == "zone_entry" {
			ss.resetToRearm(len(steps), ts)
		} else {
			ss.resetToIdle(len(steps))
		}
	}

	// Track whether this frame just transitioned from idle -> in_progress (avoid double-counting gap).
	justStarted := false
	if ss.state == "idle" {
		if triggerMode == "zone_entry" {
			if !entryPresent {
				return nil, nil, buildSOPProgress(rule, ss)
			}
			ss.state = "in_progress"
			ss.cycleStartedAt = ts
			ss.lastHeartbeatAt = ts
			ss.currentStepIdx = 0
			justStarted = true
		} else {
			// first_step mode: look for earliest confirmed step hit.
			hitIdx := firstConfirmedFrom(stepConfirmed, 0)
			if hitIdx < 0 {
				return nil, nil, buildSOPProgress(rule, ss)
			}
			missingReqIdx := firstRequiredInRange(steps, 0, hitIdx)
			if missingReqIdx >= 0 {
				// Operator jumped straight to a later step, skipping a required earlier step.
				vType := pickOrderViolationType(violationEnabled, "missing_step")
				var alarm *TriggeredAlarm
				if vType != "" && !inCooldown {
					st.lastAlarmAt = ts
					trace := buildViolationTrace(triggerMode, vType, steps, ss, missingReqIdx, hitIdx, ts, ts)
					a := buildSOPAlarm(rule, severity, ts, stepMatched[hitIdx], trace)
					alarm = &a
				}
				ss.resetToIdle(len(steps))
				return alarm, nil, buildSOPProgress(rule, ss)
			}

			// Valid start: mark leading optional steps skipped, hitIdx completed.
			ss.state = "in_progress"
			ss.cycleStartedAt = ts
			ss.lastHeartbeatAt = ts
			for k := 0; k < hitIdx; k++ {
				ss.stepStatuses[k] = "skipped"
			}
			tCopy := ts
			ss.stepStatuses[hitIdx] = "completed"
			ss.stepCompletedAt[hitIdx] = &tCopy
			ss.stepElapsedMs[hitIdx] = 0
			ss.currentStepIdx = hitIdx + 1
			justStarted = true
		}
	}

	// In-progress evaluation.
	pausedByGap := false
	if !justStarted {
		if ss.lastHeartbeatAt.IsZero() {
			ss.lastHeartbeatAt = ts
		} else {
			gap := ts.Sub(ss.lastHeartbeatAt)
			ss.lastHeartbeatAt = ts
			if gap < 0 || gap > hbTimeout {
				// Stream drop / node outage: pause & reset step timer to guarantee zero false timeouts.
				ss.stepElapsed = 0
				pausedByGap = true
			} else {
				ss.stepElapsed += gap
				ss.cycleElapsed += gap
			}
		}

		// Evaluate confirmed step hits at or after currentStepIdx.
		if ss.currentStepIdx < len(steps) {
			if stepConfirmed[ss.currentStepIdx] {
				tCopy := ts
				ss.stepStatuses[ss.currentStepIdx] = "completed"
				ss.stepCompletedAt[ss.currentStepIdx] = &tCopy
				ss.stepElapsedMs[ss.currentStepIdx] = ss.stepElapsed.Milliseconds()
				ss.stepElapsed = 0
				ss.currentStepIdx++
			} else if hitIdx := firstConfirmedFrom(stepConfirmed, ss.currentStepIdx+1); hitIdx >= 0 {
				missingReqIdx := firstRequiredInRange(steps, ss.currentStepIdx, hitIdx)
				if missingReqIdx < 0 {
					// All skipped steps are optional -> silently mark skipped and advance!
					for k := ss.currentStepIdx; k < hitIdx; k++ {
						ss.stepStatuses[k] = "skipped"
					}
					tCopy := ts
					ss.stepStatuses[hitIdx] = "completed"
					ss.stepCompletedAt[hitIdx] = &tCopy
					ss.stepElapsedMs[hitIdx] = ss.stepElapsed.Milliseconds()
					ss.stepElapsed = 0
					ss.currentStepIdx = hitIdx + 1
				} else {
					vType := pickOrderViolationType(violationEnabled, "out_of_order")
					var alarm *TriggeredAlarm
					if vType != "" && !inCooldown {
						st.lastAlarmAt = ts
						trace := buildViolationTrace(triggerMode, vType, steps, ss, missingReqIdx, hitIdx, ss.cycleStartedAt, ts)
						a := buildSOPAlarm(rule, severity, ts, stepMatched[hitIdx], trace)
						alarm = &a
					}
					finishReset()
					return alarm, nil, buildSOPProgress(rule, ss)
				}
			}
		}
	}

	// Check compliant completion (all required steps done).
	if ss.currentStepIdx >= len(steps) || !hasRequiredFrom(steps, ss.currentStepIdx) {
		for k := ss.currentStepIdx; k < len(steps); k++ {
			if ss.stepStatuses[k] == "pending" {
				ss.stepStatuses[k] = "skipped"
			}
		}
		durMs := ts.Sub(ss.cycleStartedAt).Milliseconds()
		if durMs < 0 {
			durMs = 0
		}
		completed := &CompletedSOPCycle{
			RuleID:             rule.ID,
			CameraID:           rule.CameraID,
			AlgorithmVersionID: rule.AlgorithmVersionID,
			StartedAt:          ss.cycleStartedAt,
			EndedAt:            ts,
			DurationMs:         durMs,
		}
		finishReset()
		return nil, completed, buildSOPProgress(rule, ss)
	}

	// Check step_timeout / cycle_timeout (never fire on optional steps or paused heartbeat gaps).
	if !pausedByGap && violationEnabled["step_timeout"] {
		reqIdx := firstRequiredInRange(steps, ss.currentStepIdx, len(steps))
		if reqIdx >= 0 {
			reqStep := steps[reqIdx]
			stepTimedOut := reqStep.MaxWaitSeconds > 0 && ss.stepElapsed > time.Duration(reqStep.MaxWaitSeconds)*time.Second
			cycleTimedOut := rule.Params.CycleTimeoutSeconds > 0 && ss.cycleElapsed > time.Duration(rule.Params.CycleTimeoutSeconds)*time.Second
			if stepTimedOut || cycleTimedOut {
				var alarm *TriggeredAlarm
				if !inCooldown {
					st.lastAlarmAt = ts
					trace := buildViolationTrace(triggerMode, "step_timeout", steps, ss, reqIdx, -1, ss.cycleStartedAt, ts)
					a := buildSOPAlarm(rule, severity, ts, nil, trace)
					alarm = &a
				}
				finishReset()
				return alarm, nil, buildSOPProgress(rule, ss)
			}
		}
	}

	return nil, nil, buildSOPProgress(rule, ss)
}

func filterDetectionsForSpec(dets []Detection, roi ROI, labels []string, minConf float64, hitPoint string) []Detection {
	if len(dets) == 0 {
		return nil
	}
	labelSet := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		labelSet[l] = struct{}{}
	}
	if hitPoint == "" {
		hitPoint = domain.HitPointCenter
	}
	var out []Detection
	for _, d := range dets {
		if d.Confidence < minConf {
			continue
		}
		if len(labelSet) > 0 {
			if _, ok := labelSet[d.Label]; !ok {
				continue
			}
		}
		if !DetectionHitsROI(d.BBox, roi, hitPoint) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func buildViolationSet(types []string) map[string]bool {
	if len(types) == 0 {
		return map[string]bool{
			"missing_step": true,
			"out_of_order": true,
			"step_timeout": true,
		}
	}
	m := make(map[string]bool, len(types))
	for _, t := range types {
		m[t] = true
	}
	return m
}

func pickOrderViolationType(enabled map[string]bool, preferred string) string {
	if enabled[preferred] {
		return preferred
	}
	if preferred == "missing_step" && enabled["out_of_order"] {
		return "out_of_order"
	}
	if preferred == "out_of_order" && enabled["missing_step"] {
		return "missing_step"
	}
	return ""
}

func firstConfirmedFrom(confirmed []bool, start int) int {
	for i := start; i < len(confirmed); i++ {
		if confirmed[i] {
			return i
		}
	}
	return -1
}

func firstRequiredInRange(steps []SOPStepSpec, start, end int) int {
	for i := start; i < end && i < len(steps); i++ {
		if steps[i].IsRequired() {
			return i
		}
	}
	return -1
}

func hasRequiredFrom(steps []SOPStepSpec, start int) bool {
	return firstRequiredInRange(steps, start, len(steps)) >= 0
}

func buildViolationTrace(triggerMode, violationType string, steps []SOPStepSpec, ss *sopRuntimeState, violatedIdx, hitIdx int, startedAt, endedAt time.Time) *SOPTrace {
	items := make([]SOPStepTraceItem, len(steps))
	for i, sp := range steps {
		status := ss.stepStatuses[i]
		completedAt := ss.stepCompletedAt[i]
		elapsedMs := ss.stepElapsedMs[i]
		switch i {
		case violatedIdx:
			status = "violated"
		case hitIdx:
			status = "completed"
			tCopy := endedAt
			completedAt = &tCopy
		}
		items[i] = SOPStepTraceItem{
			StepID:      sp.StepID,
			Name:        sp.Name,
			Status:      status,
			CompletedAt: completedAt,
			ElapsedMs:   elapsedMs,
		}
	}
	durMs := endedAt.Sub(startedAt).Milliseconds()
	if durMs < 0 {
		durMs = 0
	}
	var vStepID, vStepName string
	if violatedIdx >= 0 && violatedIdx < len(steps) {
		vStepID = steps[violatedIdx].StepID
		vStepName = steps[violatedIdx].Name
	}
	return &SOPTrace{
		TriggerMode:      triggerMode,
		ViolationType:    violationType,
		ViolatedStepID:   vStepID,
		ViolatedStepName: vStepName,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		DurationMs:       durMs,
		Steps:            items,
	}
}

func buildSOPAlarm(rule Rule, severity string, ts time.Time, matched []Detection, trace *SOPTrace) TriggeredAlarm {
	title := fmt.Sprintf("%s: %s (%s: %s)", rule.ROI.Name, rule.Name, trace.ViolationType, trace.ViolatedStepName)
	a := buildTriggeredAlarm(rule, severity, ts, len(matched), matched, title)
	a.SOPTrace = trace
	a.ViolationType = trace.ViolationType
	return a
}

func buildSOPProgress(rule Rule, ss *sopRuntimeState) SOPRuleProgress {
	steps := rule.Params.Steps
	items := make([]SOPStepTraceItem, len(steps))
	for i, sp := range steps {
		status := ss.stepStatuses[i]
		if ss.state == "in_progress" && i == ss.currentStepIdx && status == "pending" {
			status = "active"
		}
		items[i] = SOPStepTraceItem{
			StepID:      sp.StepID,
			Name:        sp.Name,
			Status:      status,
			CompletedAt: ss.stepCompletedAt[i],
			ElapsedMs:   ss.stepElapsedMs[i],
		}
	}
	return SOPRuleProgress{
		RuleID:           rule.ID,
		RuleName:         rule.Name,
		State:            ss.state,
		CurrentStepIndex: ss.currentStepIdx,
		Steps:            items,
	}
}
