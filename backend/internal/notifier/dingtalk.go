package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

// DingTalkConfig matches alarm_channels.config JSONB schema for channel_type='dingtalk'.
type DingTalkConfig struct {
	WebhookURL string `json:"webhook_url"`
	URL        string `json:"url"`
	Secret     string `json:"secret"`
	TimeoutMs  int    `json:"timeout_ms"`
}

func (c DingTalkConfig) targetURL() string {
	if u := strings.TrimSpace(c.WebhookURL); u != "" {
		return u
	}
	return strings.TrimSpace(c.URL)
}

// DingTalkNotifier delivers Markdown alerts to DingTalk custom robots with HMAC-SHA256 Base64 signing.
type DingTalkNotifier struct {
	client *http.Client
}

// NewDingTalkNotifier constructs a DingTalkNotifier.
func NewDingTalkNotifier(client *http.Client) *DingTalkNotifier {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &DingTalkNotifier{client: client}
}

// Type returns "dingtalk".
func (d *DingTalkNotifier) Type() string { return "dingtalk" }

// ValidateConfig checks DingTalk webhook URL validity.
func (d *DingTalkNotifier) ValidateConfig(rawConfig []byte) error {
	var cfg DingTalkConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse dingtalk config: %w", err)
	}
	u, err := url.Parse(cfg.targetURL())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("dingtalk config requires a valid http/https webhook_url")
	}
	return nil
}

// ComputeDingTalkSign computes DingTalk's base64(hmac_sha256(secret, timestampMs + "\n" + secret)).
func ComputeDingTalkSign(secret string, timestampMs int64) string {
	strToSign := fmt.Sprintf("%d\n%s", timestampMs, secret)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Send formats and dispatches a signed Markdown message to DingTalk.
func (d *DingTalkNotifier) Send(ctx context.Context, rawConfig []byte, msg Notification) (SendResult, error) {
	var cfg DingTalkConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return SendResult{}, fmt.Errorf("parse dingtalk config: %w", err)
	}
	target := cfg.targetURL()
	if target == "" {
		return SendResult{}, errors.New("dingtalk webhook_url is empty")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return SendResult{}, fmt.Errorf("invalid dingtalk webhook_url: %w", err)
	}
	if cfg.Secret != "" {
		tsMs := time.Now().UTC().UnixMilli()
		q := parsed.Query()
		q.Set("timestamp", strconv.FormatInt(tsMs, 10))
		q.Set("sign", ComputeDingTalkSign(cfg.Secret, tsMs))
		parsed.RawQuery = q.Encode()
	}

	timeoutMs := cfg.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	var md strings.Builder
	fmt.Fprintf(&md, "#### [%s] %s\n\n", strings.ToUpper(msg.Severity), msg.Title)
	fmt.Fprintf(&md, "- 摄像头：%s\n", msg.Camera.Name)
	if msg.ROI.Name != "" {
		fmt.Fprintf(&md, "- 区域：%s\n", msg.ROI.Name)
	}
	fmt.Fprintf(&md, "- 模式：%s\n", msg.RuleMode)
	fmt.Fprintf(&md, "- 时间：%s\n", msg.TriggeredAt.UTC().Format(time.RFC3339))
	if msg.Evidence.ImageURL != "" {
		fmt.Fprintf(&md, "\n![抓拍](%s)\n", msg.Evidence.ImageURL)
	}

	body, _ := json.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": msg.Title,
			"text":  md.String(),
		},
	})

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return SendResult{}, fmt.Errorf("build dingtalk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := d.client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return SendResult{LatencyMs: latencyMs}, fmt.Errorf("send dingtalk: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	res := SendResult{StatusCode: resp.StatusCode, LatencyMs: latencyMs}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("dingtalk returned HTTP %d", resp.StatusCode)
	}
	return res, nil
}
