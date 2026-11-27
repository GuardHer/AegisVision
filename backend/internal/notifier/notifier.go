package notifier

import (
	"context"
	"encoding/json"
	"time"
)

// CameraRef identifies the source camera inside a notification payload.
type CameraRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
}

// ROIRef identifies the triggered ROI region inside a notification payload.
type ROIRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// AlgorithmRef identifies the algorithm key and version inside a notification payload.
type AlgorithmRef struct {
	Key     string `json:"key,omitempty"`
	Version string `json:"version,omitempty"`
}

// EvidenceRef carries temporary HMAC-signed evidence URLs.
type EvidenceRef struct {
	ImageURL    string `json:"image_url,omitempty"`
	VideoStatus string `json:"video_status"`
	VideoURL    string `json:"video_url,omitempty"`
}

// Notification represents a single alarm notification dispatched through a Notifier plugin.
type Notification struct {
	DeliveryID    string          `json:"-"`
	AlarmID       string          `json:"alarm_id"`
	TriggeredAt   time.Time       `json:"triggered_at"`
	Severity      string          `json:"severity"`
	RuleMode      string          `json:"rule_mode"`
	Title         string          `json:"title"`
	Camera        CameraRef       `json:"camera"`
	ROI           ROIRef          `json:"roi"`
	Algorithm     AlgorithmRef    `json:"algorithm"`
	DetectedCount int             `json:"detected_count"`
	Detections    json.RawMessage `json:"detections"`
	Evidence      EvidenceRef     `json:"evidence"`
}

// SendResult captures transport outcome metrics for alarm_deliveries auditing.
type SendResult struct {
	StatusCode int
	LatencyMs  int
}

// Notifier is the extensible channel plugin interface (webhook, mqtt, modbus, sms, etc.).
type Notifier interface {
	Type() string
	ValidateConfig(rawConfig []byte) error
	Send(ctx context.Context, rawConfig []byte, msg Notification) (SendResult, error)
}
