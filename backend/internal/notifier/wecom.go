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

// WeComConfig matches alarm_channels.config JSONB schema for channel_type='wecom'.
type WeComConfig struct {
	WebhookURL string `json:"webhook_url"`
	URL        string `json:"url"`
	TimeoutMs  int    `json:"timeout_ms"`
}

func (c WeComConfig) targetURL() string {
	if u := strings.TrimSpace(c.WebhookURL); u != "" {
		return u
	}
	return strings.TrimSpace(c.URL)
}

// WeComNotifier sends Markdown alarm alerts to Enterprise WeChat group robots.
type WeComNotifier struct {
	client *http.Client
}

// NewWeComNotifier constructs a WeComNotifier.
func NewWeComNotifier(client *http.Client) *WeComNotifier {
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &WeComNotifier{client: client}
}

// Type returns "wecom".
func (w *WeComNotifier) Type() string { return "wecom" }

// ValidateConfig verifies a valid HTTP(S) webhook_url is provided.
func (w *WeComNotifier) ValidateConfig(rawConfig []byte) error {
	var cfg WeComConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse wecom config: %w", err)
	}
	u, err := url.Parse(cfg.targetURL())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("wecom config requires a valid http/https webhook_url")
	}
	return nil
}

// Send formats and dispatches a Markdown alarm notification to WeCom.
func (w *WeComNotifier) Send(ctx context.Context, rawConfig []byte, msg Notification) (SendResult, error) {
	var cfg WeComConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return SendResult{}, fmt.Errorf("parse wecom config: %w", err)
	}
	target := cfg.targetURL()
	if target == "" {
		return SendResult{}, errors.New("wecom webhook_url is empty")
	}
	timeoutMs := cfg.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	var md strings.Builder
	fmt.Fprintf(&md, "### [%s] %s\n", strings.ToUpper(msg.Severity), msg.Title)
	fmt.Fprintf(&md, "> 摄像头：%s\n", msg.Camera.Name)
	if msg.ROI.Name != "" {
		fmt.Fprintf(&md, "> 区域：%s\n", msg.ROI.Name)
	}
	fmt.Fprintf(&md, "> 规则模式：%s\n", msg.RuleMode)
	fmt.Fprintf(&md, "> 触发时间：%s\n", msg.TriggeredAt.UTC().Format(time.RFC3339))
	if msg.Evidence.ImageURL != "" {
		fmt.Fprintf(&md, "> [查看抓拍原图](%s)\n", msg.Evidence.ImageURL)
	}

	body, _ := json.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": md.String(),
		},
	})

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return SendResult{}, fmt.Errorf("build wecom request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := w.client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return SendResult{LatencyMs: latencyMs}, fmt.Errorf("send wecom: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	res := SendResult{StatusCode: resp.StatusCode, LatencyMs: latencyMs}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("wecom returned HTTP %d", resp.StatusCode)
	}
	if len(respBody) > 0 {
		var apiResp struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if json.Unmarshal(respBody, &apiResp) == nil && apiResp.ErrCode != 0 {
			return res, fmt.Errorf("wecom error code %d: %s", apiResp.ErrCode, apiResp.ErrMsg)
		}
	}
	return res, nil
}
