package scheduler

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
)

// CameraInfo is the scheduler's read model of a camera.
type CameraInfo struct {
	ID           uuid.UUID
	StreamPath   string
	SourceURL    string
	SubStreamURL string
	Status       string
	Enabled      bool
}

// VersionInfo is the scheduler's read model of an algorithm version.
type VersionInfo struct {
	ID            uuid.UUID
	AlgorithmID   uuid.UUID
	Version       string
	RuntimeKey    string
	DefaultParams []byte
	Status        string
}

// ROIInfo is the scheduler's read model of a camera ROI.
type ROIInfo struct {
	ID           uuid.UUID
	CameraID     uuid.UUID
	Name         string
	ROIType      string
	Points       []byte
	Enabled      bool
	TargetLabels []string
}

// RuleInfo is the scheduler's read model of an ROI × AlgorithmVersion rule.
type RuleInfo struct {
	ID                 uuid.UUID
	CameraID           uuid.UUID
	ROIID              uuid.UUID
	AlgorithmVersionID uuid.UUID
	RuleMode           string
	Params             []byte
	ScheduleMode       string
	PinnedNodeID       string
	AlgoParamsOverride []byte
	RecordVideo        bool
	Enabled            bool
}

// NodeInfo is the scheduler's read model of a compute node running aegis-agent.
type NodeInfo struct {
	NodeID          string
	RuntimeKeys     []string
	MaxTasks        int
	RunningTasks    int
	Status          string
	LastHeartbeatAt time.Time
}

// ExistingTaskInfo represents an existing row in inference_tasks.
type ExistingTaskInfo struct {
	ID                 uuid.UUID
	CameraID           uuid.UUID
	AlgorithmVersionID uuid.UUID
	RuntimeKey         string
	AssignedNodeID     string
	IngestInstanceID   string
	DesiredState       string
	ActualState        string
	LeaseID            uuid.NullUUID
	LeaseExpiresAt     *time.Time
	ConfigVersion      int64
	ErrorMessage       string
}

// PlanInput bundles all inputs required to compute desired deduplicated tasks and placement.
type PlanInput struct {
	Now           time.Time
	NodeTimeout   time.Duration
	Cameras       map[uuid.UUID]CameraInfo
	Versions      map[uuid.UUID]VersionInfo
	ROIs          map[uuid.UUID]ROIInfo
	Rules         []RuleInfo
	Nodes         []NodeInfo
	ExistingTasks []ExistingTaskInfo
}

// DesiredTask represents one deduplicated (camera_id, algorithm_version_id) inference task.
type DesiredTask struct {
	CameraID           uuid.UUID
	AlgorithmVersionID uuid.UUID
	StreamPath         string
	RuntimeKey         string
	ROIs               []ROIInfo
	AlgoParamsJSON     string
	RecordVideo        bool
	ScheduleMode       string
	PinnedNodeID       string
	AssignedNodeID     string
	UnassignedReason   string
}

// TaskKey uniquely identifies a deduplicated inference task.
type TaskKey struct {
	CameraID           uuid.UUID
	AlgorithmVersionID uuid.UUID
}

// IsNodeHealthy returns true if the node is online and its last heartbeat is within timeout.
func IsNodeHealthy(n NodeInfo, now time.Time, timeout time.Duration) bool {
	if n.Status != domain.NodeStatusOnline {
		return false
	}
	if n.LastHeartbeatAt.IsZero() {
		return false
	}
	if timeout > 0 && now.Sub(n.LastHeartbeatAt) > timeout {
		return false
	}
	return true
}

// ComputeDesiredTasks merges enabled rules by (camera_id, algorithm_version_id) and assigns
// each task to an eligible compute node using pinned > stickiness > camera affinity > least-load.
func ComputeDesiredTasks(in PlanInput) []DesiredTask {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nodeTimeout := in.NodeTimeout
	if nodeTimeout <= 0 {
		nodeTimeout = 15 * time.Second
	}

	type group struct {
		key          TaskKey
		cam          CameraInfo
		ver          VersionInfo
		roiSet       map[uuid.UUID]ROIInfo
		roiLabels    map[uuid.UUID]map[string]struct{}
		mergedParams map[string]any
		recordVideo  bool
		scheduleMode string
		pinnedNodeID string
	}

	addROILabels := func(g *group, roiID uuid.UUID, labels []string) {
		if len(labels) == 0 {
			return
		}
		set := g.roiLabels[roiID]
		if set == nil {
			set = make(map[string]struct{}, len(labels))
			g.roiLabels[roiID] = set
		}
		for _, raw := range labels {
			lbl := strings.TrimSpace(raw)
			if lbl != "" {
				set[lbl] = struct{}{}
			}
		}
	}

	groups := make(map[TaskKey]*group)
	for _, r := range in.Rules {
		if !r.Enabled {
			continue
		}
		cam, ok := in.Cameras[r.CameraID]
		if !ok || !cam.Enabled {
			continue
		}
		ver, ok := in.Versions[r.AlgorithmVersionID]
		if !ok || ver.Status == "deprecated" {
			continue
		}
		roi, ok := in.ROIs[r.ROIID]
		if !ok || !roi.Enabled {
			continue
		}

		key := TaskKey{CameraID: r.CameraID, AlgorithmVersionID: r.AlgorithmVersionID}
		g, exists := groups[key]
		if !exists {
			g = &group{
				key:          key,
				cam:          cam,
				ver:          ver,
				roiSet:       make(map[uuid.UUID]ROIInfo),
				roiLabels:    make(map[uuid.UUID]map[string]struct{}),
				mergedParams: parseJSONMap(ver.DefaultParams),
				scheduleMode: "auto",
			}
			groups[key] = g
		}
		g.roiSet[roi.ID] = roi

		// Extract per-ROI target behavior labels from rule params (supporting both standard & SOP rules).
		if len(r.Params) > 0 {
			var parsed ruleTargetParams
			if json.Unmarshal(r.Params, &parsed) == nil {
				addROILabels(g, roi.ID, parsed.Labels)
				addROILabels(g, roi.ID, parsed.EntryLabels)
				for _, step := range parsed.Steps {
					targetROIID := roi.ID
					if rawStepROI := strings.TrimSpace(step.ROIID); rawStepROI != "" {
						if stepUUID, err := uuid.Parse(rawStepROI); err == nil {
							if stepROI, ok := in.ROIs[stepUUID]; ok && stepROI.Enabled {
								g.roiSet[stepROI.ID] = stepROI
								targetROIID = stepROI.ID
							}
						}
					}
					addROILabels(g, targetROIID, step.Labels)
				}
			}
		}

		for k, v := range parseJSONMap(r.AlgoParamsOverride) {
			g.mergedParams[k] = v
		}
		if r.RecordVideo {
			g.recordVideo = true
		}
		if r.ScheduleMode == "pinned" && r.PinnedNodeID != "" {
			g.scheduleMode = "pinned"
			g.pinnedNodeID = r.PinnedNodeID
		}
	}

	// Build healthy node lookup and track dynamic planned load + camera affinity per node.
	type nodeState struct {
		info        NodeInfo
		runtimeSet  map[string]struct{}
		plannedLoad int
		cameraSet   map[uuid.UUID]struct{}
	}

	healthyNodes := make(map[string]*nodeState)
	for _, n := range in.Nodes {
		if !IsNodeHealthy(n, now, nodeTimeout) {
			continue
		}
		rtSet := make(map[string]struct{}, len(n.RuntimeKeys))
		for _, rk := range n.RuntimeKeys {
			rtSet[rk] = struct{}{}
		}
		healthyNodes[n.NodeID] = &nodeState{
			info:        n,
			runtimeSet:  rtSet,
			plannedLoad: 0,
			cameraSet:   make(map[uuid.UUID]struct{}),
		}
	}

	// Index existing running tasks for stickiness and camera affinity seeding.
	existingByKey := make(map[TaskKey]ExistingTaskInfo)
	for _, tk := range in.ExistingTasks {
		if tk.DesiredState != domain.TaskDesiredRunning {
			continue
		}
		existingByKey[TaskKey{CameraID: tk.CameraID, AlgorithmVersionID: tk.AlgorithmVersionID}] = tk
		if ns, ok := healthyNodes[tk.AssignedNodeID]; ok {
			ns.cameraSet[tk.CameraID] = struct{}{}
		}
	}

	// Sort keys deterministically; place pinned and sticky tasks first so affinity/capacity is respected.
	keys := make([]TaskKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		gi, gj := groups[keys[i]], groups[keys[j]]
		pi, pj := gi.scheduleMode == "pinned", gj.scheduleMode == "pinned"
		if pi != pj {
			return pi
		}
		_, si := existingByKey[keys[i]]
		_, sj := existingByKey[keys[j]]
		if si != sj {
			return si
		}
		if keys[i].CameraID.String() != keys[j].CameraID.String() {
			return keys[i].CameraID.String() < keys[j].CameraID.String()
		}
		return keys[i].AlgorithmVersionID.String() < keys[j].AlgorithmVersionID.String()
	})

	result := make([]DesiredTask, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		rois := make([]ROIInfo, 0, len(g.roiSet))
		for _, r := range g.roiSet {
			roiCopy := r
			if labelSet := g.roiLabels[r.ID]; len(labelSet) > 0 {
				lbls := make([]string, 0, len(labelSet))
				for l := range labelSet {
					lbls = append(lbls, l)
				}
				sort.Strings(lbls)
				roiCopy.TargetLabels = lbls
			}
			rois = append(rois, roiCopy)
		}
		sort.Slice(rois, func(i, j int) bool { return rois[i].ID.String() < rois[j].ID.String() })

		paramsBytes, _ := json.Marshal(g.mergedParams)
		dt := DesiredTask{
			CameraID:           g.key.CameraID,
			AlgorithmVersionID: g.key.AlgorithmVersionID,
			StreamPath:         g.cam.StreamPath,
			RuntimeKey:         g.ver.RuntimeKey,
			ROIs:               rois,
			AlgoParamsJSON:     string(paramsBytes),
			RecordVideo:        g.recordVideo,
			ScheduleMode:       g.scheduleMode,
			PinnedNodeID:       g.pinnedNodeID,
		}

		// 1. Pinned mode
		if g.scheduleMode == "pinned" {
			ns, ok := healthyNodes[g.pinnedNodeID]
			if !ok {
				dt.UnassignedReason = "pinned node is offline or draining"
				result = append(result, dt)
				continue
			}
			if _, hasRT := ns.runtimeSet[g.ver.RuntimeKey]; !hasRT {
				dt.UnassignedReason = "pinned node does not support runtime_key"
				result = append(result, dt)
				continue
			}
			if ns.info.MaxTasks > 0 && ns.plannedLoad >= ns.info.MaxTasks {
				dt.UnassignedReason = "pinned node reached max_tasks capacity"
				result = append(result, dt)
				continue
			}
			dt.AssignedNodeID = ns.info.NodeID
			ns.plannedLoad++
			ns.cameraSet[g.key.CameraID] = struct{}{}
			result = append(result, dt)
			continue
		}

		// 2. Stickiness: keep currently assigned healthy node if still eligible
		if prev, ok := existingByKey[k]; ok && prev.AssignedNodeID != "" {
			if ns, healthy := healthyNodes[prev.AssignedNodeID]; healthy {
				if _, hasRT := ns.runtimeSet[g.ver.RuntimeKey]; hasRT {
					if ns.info.MaxTasks <= 0 || ns.plannedLoad < ns.info.MaxTasks {
						dt.AssignedNodeID = ns.info.NodeID
						ns.plannedLoad++
						ns.cameraSet[g.key.CameraID] = struct{}{}
						result = append(result, dt)
						continue
					}
				}
			}
		}

		// 3. Auto placement: camera affinity first, then lowest load ratio, then deterministic node_id
		var best *nodeState
		bestHasAffinity := false
		bestRatio := 2.0

		for _, ns := range healthyNodes {
			if _, hasRT := ns.runtimeSet[g.ver.RuntimeKey]; !hasRT {
				continue
			}
			if ns.info.MaxTasks > 0 && ns.plannedLoad >= ns.info.MaxTasks {
				continue
			}
			_, hasAffinity := ns.cameraSet[g.key.CameraID]
			maxCap := ns.info.MaxTasks
			if maxCap <= 0 {
				maxCap = 1
			}
			// Combine reported baseline load + planned assignments for load awareness.
			ratio := float64(ns.plannedLoad) / float64(maxCap)

			if best == nil ||
				(hasAffinity && !bestHasAffinity) ||
				(hasAffinity == bestHasAffinity && ratio < bestRatio) ||
				(hasAffinity == bestHasAffinity && ratio == bestRatio && ns.info.NodeID < best.info.NodeID) {
				best = ns
				bestHasAffinity = hasAffinity
				bestRatio = ratio
			}
		}

		if best == nil {
			dt.UnassignedReason = "no healthy node with matching runtime_key and available capacity"
		} else {
			dt.AssignedNodeID = best.info.NodeID
			best.plannedLoad++
			best.cameraSet[g.key.CameraID] = struct{}{}
		}
		result = append(result, dt)
	}

	return result
}

type ruleTargetParams struct {
	Labels      []string `json:"labels"`
	EntryLabels []string `json:"entry_labels"`
	Steps       []struct {
		ROIID  string   `json:"roi_id"`
		Labels []string `json:"labels"`
	} `json:"steps"`
}

func parseJSONMap(raw []byte) map[string]any {
	out := make(map[string]any)
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}
