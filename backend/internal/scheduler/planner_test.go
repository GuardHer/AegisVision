package scheduler_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/scheduler"
)

func TestPlannerDeduplicatesCameraAlgorithmVersion(t *testing.T) {
	camID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	roi1ID := uuid.Must(uuid.NewV7())
	roi2ID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	input := scheduler.PlanInput{
		Now:         now,
		NodeTimeout: 15 * time.Second,
		Cameras: map[uuid.UUID]scheduler.CameraInfo{
			camID: {ID: camID, StreamPath: "cam_press_1", Enabled: true},
		},
		Versions: map[uuid.UUID]scheduler.VersionInfo{
			verID: {
				ID:            verID,
				RuntimeKey:    "py-yolo-v2",
				DefaultParams: []byte(`{"target_fps":5,"conf_threshold":0.5}`),
				Status:        "active",
			},
		},
		ROIs: map[uuid.UUID]scheduler.ROIInfo{
			roi1ID: {ID: roi1ID, CameraID: camID, Name: "Zone 1", ROIType: domain.ROITypePolygon, Points: []byte(`[{"x":0.1,"y":0.1},{"x":0.4,"y":0.1},{"x":0.4,"y":0.4}]`), Enabled: true},
			roi2ID: {ID: roi2ID, CameraID: camID, Name: "Zone 2", ROIType: domain.ROITypeFullFrame, Points: []byte(`[]`), Enabled: true},
		},
		Rules: []scheduler.RuleInfo{
			{
				ID:                 uuid.Must(uuid.NewV7()),
				CameraID:           camID,
				ROIID:              roi1ID,
				AlgorithmVersionID: verID,
				ScheduleMode:       "auto",
				AlgoParamsOverride: []byte(`{"conf_threshold":0.65}`),
				RecordVideo:        false,
				Enabled:            true,
			},
			{
				ID:                 uuid.Must(uuid.NewV7()),
				CameraID:           camID,
				ROIID:              roi2ID,
				AlgorithmVersionID: verID,
				ScheduleMode:       "auto",
				AlgoParamsOverride: []byte(`{}`),
				RecordVideo:        true,
				Enabled:            true,
			},
		},
		Nodes: []scheduler.NodeInfo{
			{
				NodeID:          "gpu-node-1",
				RuntimeKeys:     []string{"py-yolo-v2"},
				MaxTasks:        8,
				RunningTasks:    0,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now.Add(-2 * time.Second),
			},
		},
	}

	desired := scheduler.ComputeDesiredTasks(input)
	if len(desired) != 1 {
		t.Fatalf("expected 1 deduplicated DesiredTask for same (camera_id, algorithm_version_id), got %d", len(desired))
	}
	dt := desired[0]
	if len(dt.ROIs) != 2 {
		t.Fatalf("expected merged task to include 2 ROIs, got %d", len(dt.ROIs))
	}
	if !dt.RecordVideo {
		t.Errorf("expected RecordVideo=true when any rule enables video recording")
	}
	if dt.AssignedNodeID != "gpu-node-1" {
		t.Errorf("AssignedNodeID = %q, want gpu-node-1", dt.AssignedNodeID)
	}
}

func TestPlannerAffinityAndPinnedNode(t *testing.T) {
	camID := uuid.Must(uuid.NewV7())
	verA := uuid.Must(uuid.NewV7())
	verB := uuid.Must(uuid.NewV7())
	verPinned := uuid.Must(uuid.NewV7())
	roiID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	input := scheduler.PlanInput{
		Now:         now,
		NodeTimeout: 15 * time.Second,
		Cameras: map[uuid.UUID]scheduler.CameraInfo{
			camID: {ID: camID, StreamPath: "cam_affinity", Enabled: true},
		},
		Versions: map[uuid.UUID]scheduler.VersionInfo{
			verA:      {ID: verA, RuntimeKey: "py-yolo-v2", Status: "active"},
			verB:      {ID: verB, RuntimeKey: "py-action-v1", Status: "active"},
			verPinned: {ID: verPinned, RuntimeKey: "py-yolo-v2", Status: "active"},
		},
		ROIs: map[uuid.UUID]scheduler.ROIInfo{
			roiID: {ID: roiID, CameraID: camID, Name: "Full", ROIType: domain.ROITypeFullFrame, Enabled: true},
		},
		Rules: []scheduler.RuleInfo{
			{ID: uuid.Must(uuid.NewV7()), CameraID: camID, ROIID: roiID, AlgorithmVersionID: verA, ScheduleMode: "auto", Enabled: true},
			{ID: uuid.Must(uuid.NewV7()), CameraID: camID, ROIID: roiID, AlgorithmVersionID: verB, ScheduleMode: "auto", Enabled: true},
			{ID: uuid.Must(uuid.NewV7()), CameraID: camID, ROIID: roiID, AlgorithmVersionID: verPinned, ScheduleMode: "pinned", PinnedNodeID: "gpu-node-2", Enabled: true},
		},
		Nodes: []scheduler.NodeInfo{
			{
				NodeID:          "gpu-node-1",
				RuntimeKeys:     []string{"py-yolo-v2", "py-action-v1"},
				MaxTasks:        8,
				RunningTasks:    2, // slightly busier, BUT already has camera affinity via ExistingTasks
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now.Add(-1 * time.Second),
			},
			{
				NodeID:          "gpu-node-2",
				RuntimeKeys:     []string{"py-yolo-v2", "py-action-v1"},
				MaxTasks:        8,
				RunningTasks:    1,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now.Add(-1 * time.Second),
			},
		},
		ExistingTasks: []scheduler.ExistingTaskInfo{
			{
				ID:                 uuid.Must(uuid.NewV7()),
				CameraID:           camID,
				AlgorithmVersionID: verA,
				RuntimeKey:         "py-yolo-v2",
				AssignedNodeID:     "gpu-node-1",
				DesiredState:       domain.TaskDesiredRunning,
			},
		},
	}

	desired := scheduler.ComputeDesiredTasks(input)
	byVer := make(map[uuid.UUID]scheduler.DesiredTask, len(desired))
	for _, dt := range desired {
		byVer[dt.AlgorithmVersionID] = dt
	}

	if got := byVer[verA].AssignedNodeID; got != "gpu-node-1" {
		t.Errorf("verA stickiness node = %q, want gpu-node-1", got)
	}
	if got := byVer[verB].AssignedNodeID; got != "gpu-node-1" {
		t.Errorf("verB camera-affinity node = %q, want gpu-node-1 (same node as camID's existing task)", got)
	}
	if got := byVer[verPinned].AssignedNodeID; got != "gpu-node-2" {
		t.Errorf("verPinned pinned node = %q, want gpu-node-2", got)
	}
}

func TestPlannerAggregatesPerROITargetLabelsIncludingSOPSubROIs(t *testing.T) {
	camID := uuid.Must(uuid.NewV7())
	verID := uuid.Must(uuid.NewV7())
	roi1ID := uuid.Must(uuid.NewV7())
	roi2ID := uuid.Must(uuid.NewV7())
	roiSubID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	input := scheduler.PlanInput{
		Now:         now,
		NodeTimeout: 15 * time.Second,
		Cameras: map[uuid.UUID]scheduler.CameraInfo{
			camID: {ID: camID, StreamPath: "cam_multi_roi", Enabled: true},
		},
		Versions: map[uuid.UUID]scheduler.VersionInfo{
			verID: {ID: verID, RuntimeKey: "py-multi-v1", Status: "active"},
		},
		ROIs: map[uuid.UUID]scheduler.ROIInfo{
			roi1ID:   {ID: roi1ID, CameraID: camID, Name: "Safety Zone", ROIType: domain.ROITypePolygon, Enabled: true},
			roi2ID:   {ID: roi2ID, CameraID: camID, Name: "SOP Station", ROIType: domain.ROITypePolygon, Enabled: true},
			roiSubID: {ID: roiSubID, CameraID: camID, Name: "Scanner Pad", ROIType: domain.ROITypePolygon, Enabled: true},
		},
		Rules: []scheduler.RuleInfo{
			{
				ID:                 uuid.Must(uuid.NewV7()),
				CameraID:           camID,
				ROIID:              roi1ID,
				AlgorithmVersionID: verID,
				RuleMode:           domain.RuleModeAnomalyDetect,
				Params:             []byte(`{"labels":["smoking","no_helmet","smoking"]}`),
				ScheduleMode:       "auto",
				Enabled:            true,
			},
			{
				ID:                 uuid.Must(uuid.NewV7()),
				CameraID:           camID,
				ROIID:              roi2ID,
				AlgorithmVersionID: verID,
				RuleMode:           domain.RuleModeSOPSequence,
				Params: []byte(`{
					"trigger_mode": "zone_entry",
					"entry_labels": ["person"],
					"steps": [
						{"step_id":"s1","name":"ESD","labels":["wear_esd_strap"],"roi_id":""},
						{"step_id":"s2","name":"Scan","labels":["scan_barcode"],"roi_id":"` + roiSubID.String() + `"}
					]
				}`),
				ScheduleMode: "auto",
				Enabled:      true,
			},
		},
		Nodes: []scheduler.NodeInfo{
			{
				NodeID:          "gpu-node-1",
				RuntimeKeys:     []string{"py-multi-v1"},
				MaxTasks:        8,
				Status:          domain.NodeStatusOnline,
				LastHeartbeatAt: now,
			},
		},
	}

	desired := scheduler.ComputeDesiredTasks(input)
	if len(desired) != 1 {
		t.Fatalf("expected 1 deduplicated task, got %d", len(desired))
	}
	byROI := make(map[uuid.UUID]scheduler.ROIInfo, len(desired[0].ROIs))
	for _, r := range desired[0].ROIs {
		byROI[r.ID] = r
	}
	if len(byROI) != 3 {
		t.Fatalf("expected 3 ROIs (including SOP step sub-ROI), got %d", len(byROI))
	}

	assertSlicesEqual := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s target_labels = %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s target_labels[%d] = %q, want %q", name, i, got[i], want[i])
			}
		}
	}
	assertSlicesEqual("roi1", byROI[roi1ID].TargetLabels, []string{"no_helmet", "smoking"})
	assertSlicesEqual("roi2", byROI[roi2ID].TargetLabels, []string{"person", "wear_esd_strap"})
	assertSlicesEqual("roiSub", byROI[roiSubID].TargetLabels, []string{"scan_barcode"})
}
