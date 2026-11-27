package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/rules"
)

// NodeRegistration carries compute host registration metadata.
type NodeRegistration struct {
	NodeID      string
	Hostname    string
	IPAddr      string
	RuntimeKeys []string
	MaxTasks    int
	Meta        []byte
}

// AlarmChannelBinding links a persisted alarm to an enabled notification channel.
type AlarmChannelBinding struct {
	ChannelID   uuid.UUID
	ChannelType string
}

// PersistedAlarm holds the alarm record created by aegis-ingest.
type PersistedAlarm struct {
	ID                 uuid.UUID
	EventDedupKey      string
	CameraID           uuid.UUID
	ROIID              uuid.NullUUID
	RuleID             uuid.NullUUID
	AlgorithmVersionID uuid.NullUUID
	RuleMode           string
	Title              string
	Severity           string
	TriggeredAt        time.Time
	DetectedCount      int
	DetectionsSnapshot []byte
	ROISnapshot        []byte
	ImageObjectKey     string
	VideoStatus        string
}

// Repository abstracts PostgreSQL operations needed by aegis-ingest.
type Repository interface {
	UpsertAgentNode(ctx context.Context, node NodeRegistration) error
	UpdateAgentHeartbeat(ctx context.Context, nodeID string, runningTasks int, meta []byte) error
	UpdateTaskState(ctx context.Context, taskID, actualState, errMsg string) error
	UpdateTaskLastFrame(ctx context.Context, taskID string, ts time.Time) error
	CreateAlarmIdempotent(ctx context.Context, alarm PersistedAlarm) (created bool, channels []AlarmChannelBinding, err error)
	GetCameraName(ctx context.Context, cameraID uuid.UUID) string
}

// MQPublisher publishes durable alarm delivery and video evidence recording jobs.
type MQPublisher interface {
	PublishAlarm(ctx context.Context, body []byte) error
	PublishEvidenceRecord(ctx context.Context, body []byte) error
}

// RedisPublisher broadcasts transient live frames and realtime alarm alerts.
type RedisPublisher interface {
	PublishFrame(ctx context.Context, cameraID string, payload []byte) error
	PublishRealtimeAlarm(ctx context.Context, payload []byte) error
}

// ProcessorDeps holds explicit dependencies for frame & buffered event processing.
type ProcessorDeps struct {
	Engine  *rules.Engine
	Repo    Repository
	Redis   RedisPublisher
	MQ      MQPublisher
	Storage storage.ObjectStorage
	Logger  *slog.Logger
}

// Processor evaluates incoming detection frames and persists/dispatches alarms.
type Processor struct {
	engine  *rules.Engine
	repo    Repository
	redis   RedisPublisher
	mq      MQPublisher
	storage storage.ObjectStorage
	log     *slog.Logger
}

// NewProcessor constructs a frame Processor.
func NewProcessor(deps ProcessorDeps) *Processor {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Processor{
		engine:  deps.Engine,
		repo:    deps.Repo,
		redis:   deps.Redis,
		mq:      deps.MQ,
		storage: deps.Storage,
		log:     deps.Logger,
	}
}

type wsFrameMessage struct {
	Type         string            `json:"type"`
	CameraID     string            `json:"camera_id"`
	AlgorithmKey string            `json:"algorithm_key,omitempty"`
	TimestampMs  int64             `json:"timestamp_ms"`
	Detections   []rules.Detection `json:"detections"`
	ROICounts    map[string]int    `json:"roi_counts"`
}

type wsAlarmAlertMessage struct {
	Type        string `json:"type"`
	AlarmID     string `json:"alarm_id"`
	CameraID    string `json:"camera_id"`
	CameraName  string `json:"camera_name"`
	RuleMode    string `json:"rule_mode"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	TriggeredAt string `json:"triggered_at"`
	ImageURL    string `json:"image_url"`
}

type alarmQueueJob struct {
	AlarmID string `json:"alarm_id"`
}

type evidenceRecordJob struct {
	AlarmID      string    `json:"alarm_id"`
	CameraID     string    `json:"camera_id"`
	TriggeredAt  time.Time `json:"triggered_at"`
	PreSeconds   int       `json:"pre_seconds"`
	PostSeconds  int       `json:"post_seconds"`
}

// ProcessLiveFrame evaluates a live frame, broadcasts transient boxes via Redis Pub/Sub,
// and triggers durable alarms when rules fire.
func (p *Processor) ProcessLiveFrame(ctx context.Context, pbFrame *agentv1.DetectionFrame) error {
	if pbFrame == nil {
		return nil
	}
	frame := toRulesFrame(pbFrame)
	if p.repo != nil && frame.TaskID != "" {
		_ = p.repo.UpdateTaskLastFrame(ctx, frame.TaskID, frame.Timestamp)
	}

	eval := p.engine.Evaluate(frame)

	// Publish transient live frame detections to Redis Pub/Sub (never written to MQ/disk).
	if p.redis != nil && frame.CameraID != "" {
		dets := frame.Detections
		if dets == nil {
			dets = []rules.Detection{}
		}
		raw, err := json.Marshal(wsFrameMessage{
			Type:        "frame_detections",
			CameraID:    frame.CameraID,
			TimestampMs: frame.Timestamp.UnixMilli(),
			Detections:  dets,
			ROICounts:   eval.ROICounts,
		})
		if err == nil {
			if pubErr := p.redis.PublishFrame(ctx, frame.CameraID, raw); pubErr != nil {
				p.log.Warn("publish live frame to redis failed", "camera_id", frame.CameraID, "err", pubErr)
			}
		}
	}

	for _, triggered := range eval.TriggeredAlarms {
		if err := p.persistAndDispatchAlarm(ctx, triggered, "", pbFrame.GetSnapshotJpeg()); err != nil {
			p.log.Error("persist live alarm failed", "camera_id", triggered.CameraID, "rule_id", triggered.RuleID, "err", err)
		}
	}
	return nil
}

// ProcessBufferedEvent handles an offline-cached abnormal event with strict event_id idempotency.
func (p *Processor) ProcessBufferedEvent(ctx context.Context, evt *agentv1.BufferedEvent) error {
	if evt == nil || evt.GetFrame() == nil {
		return nil
	}
	pbFrame := evt.GetFrame()
	frame := toRulesFrame(pbFrame)

	eval := p.engine.Evaluate(frame)
	triggeredList := eval.TriggeredAlarms

	// If offline buffered event arrived past multi-frame rule state, still honor explicit abnormal detections.
	if len(triggeredList) == 0 && hasAbnormalDetection(frame.Detections) {
		triggeredList = []rules.TriggeredAlarm{
			{
				CameraID:           frame.CameraID,
				AlgorithmVersionID: frame.AlgorithmVersionID,
				RuleMode:           domain.RuleModeAnomalyDetect,
				Title:              "Offline buffered abnormal event",
				Severity:           domain.SeverityWarning,
				TriggeredAt:        frame.Timestamp,
				DetectedCount:      len(frame.Detections),
				MatchedDetections:  frame.Detections,
			},
		}
	}

	for i, triggered := range triggeredList {
		dedupKey := evt.GetEventId()
		if i > 0 && dedupKey != "" {
			dedupKey = fmt.Sprintf("%s:%d", dedupKey, i)
		}
		if err := p.persistAndDispatchAlarm(ctx, triggered, dedupKey, pbFrame.GetSnapshotJpeg()); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) persistAndDispatchAlarm(ctx context.Context, triggered rules.TriggeredAlarm, dedupKey string, snapshotJPEG []byte) error {
	camID, err := uuid.Parse(triggered.CameraID)
	if err != nil {
		return fmt.Errorf("invalid camera_id %q: %w", triggered.CameraID, err)
	}

	alarmID := uuid.Must(uuid.NewV7())
	imageKey := ""
	if len(snapshotJPEG) > 0 && p.storage != nil {
		imageKey = fmt.Sprintf("images/%s/%s/%s.jpg",
			triggered.TriggeredAt.UTC().Format("2006/01/02"),
			camID.String(),
			alarmID.String(),
		)
		if err := p.storage.PutObject(ctx, imageKey, bytes.NewReader(snapshotJPEG), int64(len(snapshotJPEG)), "image/jpeg"); err != nil {
			p.log.Warn("upload snapshot jpeg failed", "alarm_id", alarmID, "err", err)
			imageKey = ""
		}
	}

	detsJSON, _ := json.Marshal(triggered.MatchedDetections)
	if len(detsJSON) == 0 || string(detsJSON) == "null" {
		detsJSON = []byte(`[]`)
	}
	roiJSON, _ := json.Marshal(triggered.ROI)
	if len(roiJSON) == 0 {
		roiJSON = []byte(`{}`)
	}

	videoStatus := domain.VideoStatusNone
	if triggered.RecordVideo {
		videoStatus = domain.VideoStatusRecording
	}

	persisted := PersistedAlarm{
		ID:                 alarmID,
		EventDedupKey:      dedupKey,
		CameraID:           camID,
		ROIID:              parseNullUUID(triggered.ROI.ID),
		RuleID:             parseNullUUID(triggered.RuleID),
		AlgorithmVersionID: parseNullUUID(triggered.AlgorithmVersionID),
		RuleMode:           triggered.RuleMode,
		Title:              triggered.Title,
		Severity:           triggered.Severity,
		TriggeredAt:        triggered.TriggeredAt,
		DetectedCount:      triggered.DetectedCount,
		DetectionsSnapshot: detsJSON,
		ROISnapshot:        roiJSON,
		ImageObjectKey:     imageKey,
		VideoStatus:        videoStatus,
	}

	created, _, err := p.repo.CreateAlarmIdempotent(ctx, persisted)
	if err != nil {
		return fmt.Errorf("persist alarm: %w", err)
	}
	if !created {
		// Duplicate buffered event_id: clean up orphan uploaded snapshot and do NOT republish!
		if imageKey != "" && p.storage != nil {
			_ = p.storage.DeleteObject(ctx, imageKey)
		}
		return nil
	}

	// 1. Broadcast realtime WebSocket alert immediately via Redis Pub/Sub
	if p.redis != nil {
		alertRaw, _ := json.Marshal(wsAlarmAlertMessage{
			Type:        "alarm_alert",
			AlarmID:     alarmID.String(),
			CameraID:    camID.String(),
			CameraName:  p.repo.GetCameraName(ctx, camID),
			RuleMode:    triggered.RuleMode,
			Title:       triggered.Title,
			Severity:    triggered.Severity,
			TriggeredAt: triggered.TriggeredAt.UTC().Format(time.RFC3339),
			ImageURL:    fmt.Sprintf("/api/v1/evidence/%s/image", alarmID.String()),
		})
		if pubErr := p.redis.PublishRealtimeAlarm(ctx, alertRaw); pubErr != nil {
			p.log.Warn("publish realtime alarm to redis failed", "alarm_id", alarmID, "err", pubErr)
		}
	}

	// 2. Enqueue durable alarm notification delivery (non-blocking)
	if p.mq != nil {
		jobRaw, _ := json.Marshal(alarmQueueJob{AlarmID: alarmID.String()})
		if pubErr := p.mq.PublishAlarm(ctx, jobRaw); pubErr != nil {
			p.log.Error("publish alarm to rabbitmq failed", "alarm_id", alarmID, "err", pubErr)
		}

		// 3. Enqueue asynchronous MP4 video evidence recording (never blocks alarm delivery)
		if triggered.RecordVideo {
			preSec := triggered.VideoPreSeconds
			if preSec <= 0 {
				preSec = 5
			}
			postSec := triggered.VideoPostSeconds
			if postSec <= 0 {
				postSec = 5
			}
			recRaw, _ := json.Marshal(evidenceRecordJob{
				AlarmID:     alarmID.String(),
				CameraID:    camID.String(),
				TriggeredAt: triggered.TriggeredAt,
				PreSeconds:  preSec,
				PostSeconds: postSec,
			})
			if pubErr := p.mq.PublishEvidenceRecord(ctx, recRaw); pubErr != nil {
				p.log.Error("publish evidence record job failed", "alarm_id", alarmID, "err", pubErr)
			}
		}
	}

	return nil
}

func toRulesFrame(pb *agentv1.DetectionFrame) rules.DetectionFrame {
	ts := time.UnixMilli(pb.GetTimestampMs()).UTC()
	if pb.GetTimestampMs() == 0 {
		ts = time.Now().UTC()
	}
	dets := make([]rules.Detection, 0, len(pb.GetDetections()))
	for _, d := range pb.GetDetections() {
		var bbox [4]float64
		if coords := d.GetBbox(); len(coords) >= 4 {
			bbox = [4]float64{float64(coords[0]), float64(coords[1]), float64(coords[2]), float64(coords[3])}
		}
		dets = append(dets, rules.Detection{
			BBox:       bbox,
			Label:      d.GetLabel(),
			Confidence: float64(d.GetConfidence()),
			TrackID:    d.GetTrackId(),
			IsAbnormal: d.GetIsAbnormal(),
			Attributes: d.GetAttributes(),
		})
	}
	return rules.DetectionFrame{
		TaskID:             pb.GetTaskId(),
		CameraID:           pb.GetCameraId(),
		AlgorithmVersionID: pb.GetAlgorithmVersionId(),
		Timestamp:          ts,
		IsHeartbeat:        pb.GetIsHeartbeat(),
		Detections:         dets,
	}
}

func hasAbnormalDetection(dets []rules.Detection) bool {
	for _, d := range dets {
		if d.IsAbnormal {
			return true
		}
	}
	return false
}

func parseNullUUID(raw string) uuid.NullUUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: id, Valid: true}
}
