package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WebhookConfig matches alarm_channels.config JSONB schema for channel_type='webhook'.
type WebhookConfig struct {
	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	Headers             map[string]string `json:"headers"`
	Secret              string            `json:"secret"`
	TimeoutMs           int               `json:"timeout_ms"`
	IncludeEvidenceURLs *bool             `json:"include_evidence_urls"`
}

// WebhookNotifier delivers alarm.triggered payloads over HTTP with optional HMAC-SHA256 signing.
type WebhookNotifier struct {
	client *http.Client
}

// NewWebhookNotifier constructs a WebhookNotifier.
func NewWebhookNotifier(client *http.Client) *WebhookNotifier {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &WebhookNotifier{client: client}
}

// Type returns "webhook".
func (w *WebhookNotifier) Type() string {
	return "webhook"
}

// ValidateConfig checks required URL format and HTTP method.
func (w *WebhookNotifier) ValidateConfig(rawConfig []byte) error {
	var cfg WebhookConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse webhook config: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("webhook config requires a valid http/https url")
	}
	return nil
}

type webhookBody struct {
	Event         string          `json:"event"`
	AlarmID       string          `json:"alarm_id"`
	TriggeredAt   string          `json:"triggered_at"`
	Severity      string          `json:"severity"`
	RuleMode      string          `json:"rule_mode"`
	Title         string          `json:"title"`
	Camera        CameraRef       `json:"camera"`
	ROI           ROIRef          `json:"roi"`
	Algorithm     AlgorithmRef    `json:"algorithm"`
	DetectedCount int             `json:"detected_count"`
	Detections    json.RawMessage `json:"detections"`
	Evidence      *EvidenceRef    `json:"evidence,omitempty"`
}

// Send dispatches the alarm notification to the configured webhook endpoint.
func (w *WebhookNotifier) Send(ctx context.Context, rawConfig []byte, msg Notification) (SendResult, error) {
	var cfg WebhookConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return SendResult{}, fmt.Errorf("parse webhook config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return SendResult{}, errors.New("webhook url is empty")
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	timeoutMs := cfg.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	dets := msg.Detections
	if len(dets) == 0 {
		dets = json.RawMessage(`[]`)
	}
	includeEvidence := true
	if cfg.IncludeEvidenceURLs != nil {
		includeEvidence = *cfg.IncludeEvidenceURLs
	}

	payload := webhookBody{
		Event:         "alarm.triggered",
		AlarmID:       msg.AlarmID,
		TriggeredAt:   msg.TriggeredAt.UTC().Format(time.RFC3339),
		Severity:      msg.Severity,
		RuleMode:      msg.RuleMode,
		Title:         msg.Title,
		Camera:        msg.Camera,
		ROI:           msg.ROI,
		Algorithm:     msg.Algorithm,
		DetectedCount: msg.DetectedCount,
		Detections:    dets,
	}
	if includeEvidence {
		ev := msg.Evidence
		payload.Evidence = &ev
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("marshal webhook body: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, cfg.URL, bytes.NewReader(rawBody))
	if err != nil {
		return SendResult{}, fmt.Errorf("build webhook request: %w", err)
	}

	tsStr := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aegis-Event", "alarm.triggered")
	req.Header.Set("X-Aegis-Delivery-Id", msg.DeliveryID)
	req.Header.Set("X-Aegis-Timestamp", tsStr)
	if cfg.Secret != "" {
		req.Header.Set("X-Aegis-Signature", signWebhookPayload(cfg.Secret, tsStr, rawBody))
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := w.client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return SendResult{LatencyMs: latencyMs}, fmt.Errorf("send webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	res := SendResult{
		StatusCode: resp.StatusCode,
		LatencyMs:  latencyMs,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return res, nil
}

func signWebhookPayload(secret, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(rawBody)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
