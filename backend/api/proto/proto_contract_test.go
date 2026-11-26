package proto_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	runtimev1 "aegisvision/backend/gen/go/runtime/v1"
)

func TestAgentAndRuntimeProtoRoundTrip(t *testing.T) {
	// 1. Verify PlatformDownstream AssignTaskCommand carries all required fields.
	down := &agentv1.PlatformDownstream{
		Payload: &agentv1.PlatformDownstream_AssignTask{
			AssignTask: &agentv1.AssignTaskCommand{
				CommandId:          "cmd-001",
				TaskId:             "task-001",
				CameraId:           "cam-001",
				AlgorithmVersionId: "ver-001",
				RuntimeKey:         "py-yolo-v2",
				StreamUrl:          "rtsp://mediamtx:8554/cam_001?token=short-lived",
				StreamProtocol:     "rtsp",
				Rois: []*agentv1.ROI{
					{
						RoiId:   "roi-001",
						Name:    "Zone A",
						RoiType: "polygon",
						Points: []*agentv1.Point{
							{X: 0.1, Y: 0.2},
							{X: 0.8, Y: 0.2},
							{X: 0.8, Y: 0.9},
							{X: 0.1, Y: 0.9},
						},
					},
				},
				AlgoParamsJson:  `{"target_fps":5,"conf_threshold":0.5}`,
				LeaseId:         "lease-001",
				LeaseTtlSeconds: 30,
				ConfigVersion:   2,
			},
		},
	}

	rawDown, err := proto.Marshal(down)
	if err != nil {
		t.Fatalf("marshal PlatformDownstream: %v", err)
	}
	var gotDown agentv1.PlatformDownstream
	if err := proto.Unmarshal(rawDown, &gotDown); err != nil {
		t.Fatalf("unmarshal PlatformDownstream: %v", err)
	}
	assign := gotDown.GetAssignTask()
	if assign == nil {
		t.Fatal("expected AssignTask payload, got nil")
	}
	if assign.GetStreamUrl() != "rtsp://mediamtx:8554/cam_001?token=short-lived" ||
		assign.GetStreamProtocol() != "rtsp" ||
		assign.GetLeaseId() != "lease-001" ||
		assign.GetLeaseTtlSeconds() != 30 ||
		assign.GetAlgoParamsJson() != `{"target_fps":5,"conf_threshold":0.5}` ||
		len(assign.GetRois()) != 1 || len(assign.GetRois()[0].GetPoints()) != 4 {
		t.Fatalf("unexpected AssignTaskCommand after round-trip: %+v", assign)
	}

	// 2. Verify BufferedEvent preserves event_id and raw snapshot_jpeg bytes losslessly.
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	evt := &agentv1.BufferedEvent{
		EventId:    "evt-uuid-1234",
		CachedAtMs: 1795700000123,
		Frame: &agentv1.DetectionFrame{
			TaskId:             "task-001",
			CameraId:           "cam-001",
			AlgorithmVersionId: "ver-001",
			TimestampMs:        1795700000100,
			FrameWidth:         1920,
			FrameHeight:        1080,
			IsHeartbeat:        false,
			Detections: []*agentv1.Detection{
				{
					Bbox:       []float32{0.1, 0.2, 0.3, 0.8},
					Label:      "no_helmet",
					Confidence: 0.93,
					TrackId:    "trk-7",
					IsAbnormal: true,
					Attributes: map[string]string{"color": "red"},
				},
			},
			SnapshotJpeg: jpegBytes,
		},
	}

	rawEvt, err := proto.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal BufferedEvent: %v", err)
	}
	var gotEvt agentv1.BufferedEvent
	if err := proto.Unmarshal(rawEvt, &gotEvt); err != nil {
		t.Fatalf("unmarshal BufferedEvent: %v", err)
	}
	if gotEvt.GetEventId() != "evt-uuid-1234" {
		t.Errorf("EventId = %q, want evt-uuid-1234", gotEvt.GetEventId())
	}
	if !bytes.Equal(gotEvt.GetFrame().GetSnapshotJpeg(), jpegBytes) {
		t.Errorf("SnapshotJpeg corrupted during round-trip")
	}

	// 3. Verify RuntimeAssignTaskRequest & RuntimeResult round-trip.
	rtReq := &runtimev1.RuntimeAssignTaskRequest{
		TaskId:             "task-001",
		CameraId:           "cam-001",
		AlgorithmVersionId: "ver-001",
		StreamUrl:          "rtsp://mediamtx:8554/cam_001?token=short-lived",
		StreamProtocol:     "rtsp",
		LeaseId:            "lease-001",
		LeaseTtlSeconds:    30,
		AlgoParamsJson:     `{"target_fps":5}`,
		ConfigVersion:      1,
	}
	rawRT, err := proto.Marshal(rtReq)
	if err != nil {
		t.Fatalf("marshal RuntimeAssignTaskRequest: %v", err)
	}
	var gotRT runtimev1.RuntimeAssignTaskRequest
	if err := proto.Unmarshal(rawRT, &gotRT); err != nil {
		t.Fatalf("unmarshal RuntimeAssignTaskRequest: %v", err)
	}
	if gotRT.GetLeaseId() != "lease-001" || gotRT.GetLeaseTtlSeconds() != 30 {
		t.Fatalf("unexpected RuntimeAssignTaskRequest after round-trip: %+v", &gotRT)
	}

	rtRes := &runtimev1.RuntimeResult{
		TaskId:             "task-001",
		CameraId:           "cam-001",
		AlgorithmVersionId: "ver-001",
		TimestampMs:        1795700000500,
		IsHeartbeat:        true,
	}
	rawRes, err := proto.Marshal(rtRes)
	if err != nil {
		t.Fatalf("marshal RuntimeResult: %v", err)
	}
	var gotRes runtimev1.RuntimeResult
	if err := proto.Unmarshal(rawRes, &gotRes); err != nil {
		t.Fatalf("unmarshal RuntimeResult: %v", err)
	}
	if !gotRes.GetIsHeartbeat() || gotRes.GetTaskId() != "task-001" {
		t.Fatalf("unexpected RuntimeResult after round-trip: %+v", &gotRes)
	}
}
