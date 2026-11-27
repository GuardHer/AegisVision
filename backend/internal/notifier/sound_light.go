package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SoundLightConfig matches alarm_channels.config JSONB schema for channel_type='sound_light'.
type SoundLightConfig struct {
	TriggerURL        string            `json:"trigger_url"`
	ResetURL          string            `json:"reset_url"`
	Method            string            `json:"method"`
	Headers           map[string]string `json:"headers"`
	BodyTemplate      string            `json:"body_template"`
	ResetBodyTemplate string            `json:"reset_body_template"`
	AutoResetSeconds  int               `json:"auto_reset_seconds"`
	TimeoutMs         int               `json:"timeout_ms"`
}

// SoundLightNotifier triggers and resets on-site networked sound-light alarm boxes over HTTP.
type SoundLightNotifier struct {
	client *http.Client
}

// NewSoundLightNotifier constructs a SoundLightNotifier.
func NewSoundLightNotifier(client *http.Client) *SoundLightNotifier {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &SoundLightNotifier{client: client}
}

// Type returns "sound_light".
func (s *SoundLightNotifier) Type() string { return "sound_light" }

// ValidateConfig checks trigger_url (required) and optional reset_url.
func (s *SoundLightNotifier) ValidateConfig(rawConfig []byte) error {
	var cfg SoundLightConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse sound_light config: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(cfg.TriggerURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("sound_light config requires a valid http/https trigger_url")
	}
	if rRaw := strings.TrimSpace(cfg.ResetURL); rRaw != "" {
		ru, rErr := url.Parse(rRaw)
		if rErr != nil || (ru.Scheme != "http" && ru.Scheme != "https") || ru.Host == "" {
			return errors.New("sound_light reset_url must be a valid http/https url")
		}
	}
	return nil
}

// Send invokes the alarm box trigger_url using body_template or a default JSON payload.
func (s *SoundLightNotifier) Send(ctx context.Context, rawConfig []byte, msg Notification) (SendResult, error) {
	var cfg SoundLightConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return SendResult{}, fmt.Errorf("parse sound_light config: %w", err)
	}
	target := strings.TrimSpace(cfg.TriggerURL)
	if target == "" {
		return SendResult{}, errors.New("sound_light trigger_url is empty")
	}
	var body []byte
	if strings.TrimSpace(cfg.BodyTemplate) != "" {
		rendered := renderAlarmTemplate(cfg.BodyTemplate, msg)
		body = []byte(rendered)
	} else {
		body, _ = json.Marshal(map[string]any{
			"action":      "trigger",
			"alarm_id":    msg.AlarmID,
			"camera_name": msg.Camera.Name,
			"severity":    msg.Severity,
			"rule_mode":   msg.RuleMode,
			"title":       msg.Title,
		})
	}
	return s.doHTTP(ctx, target, cfg.Method, cfg.Headers, cfg.TimeoutMs, body)
}

// Reset silences/resets the alarm box via reset_url.
func (s *SoundLightNotifier) Reset(ctx context.Context, rawConfig []byte) (SendResult, error) {
	var cfg SoundLightConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return SendResult{}, fmt.Errorf("parse sound_light config: %w", err)
	}
	target := strings.TrimSpace(cfg.ResetURL)
	if target == "" {
		return SendResult{}, nil
	}
	body := []byte(`{"action":"reset"}`)
	if strings.TrimSpace(cfg.ResetBodyTemplate) != "" {
		body = []byte(cfg.ResetBodyTemplate)
	}
	return s.doHTTP(ctx, target, cfg.Method, cfg.Headers, cfg.TimeoutMs, body)
}

func (s *SoundLightNotifier) doHTTP(ctx context.Context, target, method string, headers map[string]string, timeoutMs int, body []byte) (SendResult, error) {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = http.MethodPost
	}
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var bodyReader io.Reader
	if m != http.MethodGet {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, m, target, bodyReader)
	if err != nil {
		return SendResult{}, fmt.Errorf("build sound_light request: %w", err)
	}
	if m != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return SendResult{LatencyMs: latencyMs}, fmt.Errorf("send sound_light: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	res := SendResult{StatusCode: resp.StatusCode, LatencyMs: latencyMs}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("sound_light returned HTTP %d", resp.StatusCode)
	}
	return res, nil
}

func renderAlarmTemplate(tmpl string, msg Notification) string {
	esc := func(v string) string {
		b, _ := json.Marshal(v)
		if len(b) >= 2 {
			return string(b[1 : len(b)-1])
		}
		return v
	}
	replacer := strings.NewReplacer(
		"{{alarm_id}}", esc(msg.AlarmID),
		"{{camera_name}}", esc(msg.Camera.Name),
		"{{title}}", esc(msg.Title),
		"{{severity}}", esc(msg.Severity),
		"{{rule_mode}}", esc(msg.RuleMode),
	)
	return replacer.Replace(tmpl)
}
