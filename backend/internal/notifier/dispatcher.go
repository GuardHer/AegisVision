package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
)

// AlarmContext holds enriched alarm metadata required to render outbound notifications.
type AlarmContext struct {
	AlarmID        uuid.UUID
	TriggeredAt    time.Time
	Severity       string
	RuleMode       string
	Title          string
	Camera         CameraRef
	ROI            ROIRef
	Algorithm      AlgorithmRef
	DetectedCount  int
	Detections     json.RawMessage
	ImageObjectKey string
	VideoObjectKey string
	VideoStatus    string
}

// DeliveryRecord represents one alarm_deliveries row joined with its alarm_channels config.
type DeliveryRecord struct {
	ID             uuid.UUID
	AlarmID        uuid.UUID
	ChannelID      uuid.UUID
	ChannelType    string
	ChannelConfig  []byte
	Status         string
	Attempts       int
	MaxAttempts    int
	LastStatusCode int
	LastLatencyMs  int
	LastError      string
}

// DeliveryUpdate describes an atomic status update written back to alarm_deliveries.
type DeliveryUpdate struct {
	DeliveryID     uuid.UUID
	Status         string
	Attempts       int
	LastStatusCode int
	LastLatencyMs  int
	LastError      string
}

// DeliveryStore abstracts PostgreSQL persistence for alarm dispatch.
type DeliveryStore interface {
	GetAlarmContext(ctx context.Context, alarmID uuid.UUID) (AlarmContext, error)
	ListDeliveriesForAlarm(ctx context.Context, alarmID uuid.UUID) ([]DeliveryRecord, error)
	GetDelivery(ctx context.Context, deliveryID uuid.UUID) (DeliveryRecord, error)
	UpdateDeliveryResult(ctx context.Context, update DeliveryUpdate) error
}

// RetryPublisher routes failed single-channel deliveries onto RabbitMQ DLX retry/DLQ queues.
type RetryPublisher interface {
	PublishAlarmRetry(ctx context.Context, failedAttempts int, body []byte) error
}

// DispatcherDeps holds explicit dependencies for Dispatcher.
type DispatcherDeps struct {
	Registry      *Registry
	Store         DeliveryStore
	RetryBus      RetryPublisher
	Signer        *auth.EvidenceSigner
	PublicBaseURL string
	EvidenceTTL   time.Duration
	Logger        *slog.Logger
}

// Dispatcher consumes alarms.triggered jobs and dispatches notifications via registered Notifier plugins.
type Dispatcher struct {
	registry      *Registry
	store         DeliveryStore
	retryBus      RetryPublisher
	signer        *auth.EvidenceSigner
	publicBaseURL string
	evidenceTTL   time.Duration
	log           *slog.Logger
}

// NewDispatcher constructs a Dispatcher.
func NewDispatcher(deps DispatcherDeps) *Dispatcher {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.EvidenceTTL <= 0 {
		deps.EvidenceTTL = 24 * time.Hour
	}
	return &Dispatcher{
		registry:      deps.Registry,
		store:         deps.Store,
		retryBus:      deps.RetryBus,
		signer:        deps.Signer,
		publicBaseURL: deps.PublicBaseURL,
		evidenceTTL:   deps.EvidenceTTL,
		log:           deps.Logger,
	}
}

type dispatchMessage struct {
	AlarmID    string `json:"alarm_id"`
	DeliveryID string `json:"delivery_id,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	Manual     bool   `json:"manual,omitempty"`
}

// HandleMessage processes an alarms.triggered RabbitMQ message (either initial alarm fan-out or single delivery retry).
func (d *Dispatcher) HandleMessage(ctx context.Context, body []byte) error {
	var msg dispatchMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		d.log.Warn("drop malformed alarm dispatch message", "err", err)
		return nil
	}

	alarmID, err := uuid.Parse(msg.AlarmID)
	if err != nil {
		d.log.Warn("drop alarm dispatch message with invalid alarm_id", "alarm_id", msg.AlarmID)
		return nil
	}

	alarmCtx, err := d.store.GetAlarmContext(ctx, alarmID)
	if err != nil {
		return fmt.Errorf("load alarm context %s: %w", alarmID, err)
	}

	var deliveries []DeliveryRecord
	if msg.DeliveryID != "" {
		delID, parseErr := uuid.Parse(msg.DeliveryID)
		if parseErr != nil {
			d.log.Warn("drop message with invalid delivery_id", "delivery_id", msg.DeliveryID)
			return nil
		}
		rec, getErr := d.store.GetDelivery(ctx, delID)
		if getErr != nil {
			return fmt.Errorf("load delivery %s: %w", delID, getErr)
		}
		deliveries = []DeliveryRecord{rec}
	} else {
		deliveries, err = d.store.ListDeliveriesForAlarm(ctx, alarmID)
		if err != nil {
			return fmt.Errorf("list deliveries for alarm %s: %w", alarmID, err)
		}
	}

	// Fan out deliveries concurrently so one hanging webhook never delays another channel.
	var wg sync.WaitGroup
	for _, rec := range deliveries {
		if !msg.Manual && rec.Status == "success" {
			continue
		}
		wg.Add(1)
		go func(delivery DeliveryRecord) {
			defer wg.Done()
			d.deliverOne(ctx, alarmCtx, delivery)
		}(rec)
	}
	wg.Wait()
	return nil
}

func (d *Dispatcher) deliverOne(ctx context.Context, alarm AlarmContext, rec DeliveryRecord) {
	plugin, ok := d.registry.Get(rec.ChannelType)
	if !ok {
		_ = d.store.UpdateDeliveryResult(ctx, DeliveryUpdate{
			DeliveryID: rec.ID,
			Status:     "dead_letter",
			Attempts:   rec.Attempts + 1,
			LastError:  fmt.Sprintf("unsupported channel type %q", rec.ChannelType),
		})
		return
	}

	notification := d.buildNotification(alarm, rec.ID)
	res, sendErr := plugin.Send(ctx, rec.ChannelConfig, notification)
	nextAttempts := rec.Attempts + 1
	maxAttempts := rec.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}

	if sendErr == nil {
		if err := d.store.UpdateDeliveryResult(ctx, DeliveryUpdate{
			DeliveryID:     rec.ID,
			Status:         "success",
			Attempts:       nextAttempts,
			LastStatusCode: res.StatusCode,
			LastLatencyMs:  res.LatencyMs,
			LastError:      "",
		}); err != nil {
			d.log.Warn("update delivery success failed", "delivery_id", rec.ID, "err", err)
		}
		return
	}

	nextStatus := "failed"
	if nextAttempts >= maxAttempts {
		nextStatus = "dead_letter"
	}

	if err := d.store.UpdateDeliveryResult(ctx, DeliveryUpdate{
		DeliveryID:     rec.ID,
		Status:         nextStatus,
		Attempts:       nextAttempts,
		LastStatusCode: res.StatusCode,
		LastLatencyMs:  res.LatencyMs,
		LastError:      sendErr.Error(),
	}); err != nil {
		d.log.Warn("update delivery failure failed", "delivery_id", rec.ID, "err", err)
	}

	if d.retryBus != nil {
		retryBody, _ := json.Marshal(dispatchMessage{
			AlarmID:    rec.AlarmID.String(),
			DeliveryID: rec.ID.String(),
			ChannelID:  rec.ChannelID.String(),
		})
		// nextAttempts == 1 -> retry.3s, 2 -> retry.15s, 3 -> retry.60s, >=4 -> alarms.dlq
		if err := d.retryBus.PublishAlarmRetry(ctx, nextAttempts, retryBody); err != nil {
			d.log.Error("publish delivery retry to DLX failed", "delivery_id", rec.ID, "attempt", nextAttempts, "err", err)
		}
	}
}

func (d *Dispatcher) buildNotification(alarm AlarmContext, deliveryID uuid.UUID) Notification {
	ev := EvidenceRef{
		VideoStatus: alarm.VideoStatus,
	}
	if d.signer != nil && d.publicBaseURL != "" {
		if alarm.ImageObjectKey != "" {
			ev.ImageURL = d.signer.SignEvidenceURL(d.publicBaseURL, alarm.AlarmID.String(), "image", d.evidenceTTL)
		}
		if alarm.VideoStatus != "" && alarm.VideoStatus != "none" {
			ev.VideoURL = d.signer.SignEvidenceURL(d.publicBaseURL, alarm.AlarmID.String(), "video", d.evidenceTTL)
		}
	}

	return Notification{
		DeliveryID:    deliveryID.String(),
		AlarmID:       alarm.AlarmID.String(),
		TriggeredAt:   alarm.TriggeredAt,
		Severity:      alarm.Severity,
		RuleMode:      alarm.RuleMode,
		Title:         alarm.Title,
		Camera:        alarm.Camera,
		ROI:           alarm.ROI,
		Algorithm:     alarm.Algorithm,
		DetectedCount: alarm.DetectedCount,
		Detections:    alarm.Detections,
		Evidence:      ev,
	}
}
