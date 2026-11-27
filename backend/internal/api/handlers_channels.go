package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// ChannelsHandler manages external notification channels (webhook, wecom, dingtalk, sound_light).
type ChannelsHandler struct {
	store      *postgres.Store
	pub        EventPublisher
	httpClient *http.Client
	log        *slog.Logger
}

// NewChannelsHandler constructs a ChannelsHandler.
func NewChannelsHandler(store *postgres.Store, pub EventPublisher, httpClient *http.Client, log *slog.Logger) *ChannelsHandler {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &ChannelsHandler{
		store:      store,
		pub:        pub,
		httpClient: httpClient,
		log:        log,
	}
}

type createChannelRequest struct {
	Name         string          `json:"name" binding:"required"`
	ChannelType  string          `json:"channel_type" binding:"required"`
	Config       json.RawMessage `json:"config" binding:"required"`
	RoutingRules json.RawMessage `json:"routing_rules"`
	Enabled      *bool           `json:"enabled"`
}

type updateChannelRequest struct {
	Name         string          `json:"name" binding:"required"`
	ChannelType  string          `json:"channel_type" binding:"required"`
	Config       json.RawMessage `json:"config" binding:"required"`
	RoutingRules json.RawMessage `json:"routing_rules"`
	Enabled      bool            `json:"enabled"`
}

type channelTestConfig struct {
	URL        string            `json:"url"`
	WebhookURL string            `json:"webhook_url"`
	TriggerURL string            `json:"trigger_url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Secret     string            `json:"secret"`
}

func isValidChannelType(ct string) bool {
	switch ct {
	case "webhook", "wecom", "dingtalk", "sound_light":
		return true
	default:
		return false
	}
}

func validateHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, domain.NewInvalidArgumentError("channel url must use valid http or https scheme", err)
	}
	return u, nil
}

// List returns all configured alarm channels.
func (h *ChannelsHandler) List(c *gin.Context) {
	items, err := h.store.Queries().ListAlarmChannels(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// Create inserts a new alarm channel with optional explicit routing_rules.
func (h *ChannelsHandler) Create(c *gin.Context) {
	var req createChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel payload", err))
		return
	}
	if !isValidChannelType(req.ChannelType) {
		respondErr(c, domain.NewInvalidArgumentError("channel_type must be webhook, wecom, dingtalk, or sound_light", nil))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	routing := req.RoutingRules
	if len(routing) == 0 {
		routing = json.RawMessage(`{"auto_route":false}`)
	}

	item, err := h.store.Queries().CreateAlarmChannel(c.Request.Context(), sqlcgen.CreateAlarmChannelParams{
		ID:           uuid.Must(uuid.NewV7()),
		Name:         strings.TrimSpace(req.Name),
		ChannelType:  req.ChannelType,
		Config:       req.Config,
		RoutingRules: routing,
		Enabled:      enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	recordAudit(c, h.store, h.log, "channel.create", "alarm_channel", item.ID.String(), "Created channel "+item.Name, map[string]any{
		"name":         item.Name,
		"channel_type": item.ChannelType,
		"config":       req.Config,
	})
	respondCreated(c, item)
}

// Update modifies an existing alarm channel.
func (h *ChannelsHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel id", err))
		return
	}
	var req updateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel update payload", err))
		return
	}
	if !isValidChannelType(req.ChannelType) {
		respondErr(c, domain.NewInvalidArgumentError("channel_type must be webhook, wecom, dingtalk, or sound_light", nil))
		return
	}

	item, err := h.store.Queries().UpdateAlarmChannel(c.Request.Context(), sqlcgen.UpdateAlarmChannelParams{
		ID:           id,
		Name:         strings.TrimSpace(req.Name),
		ChannelType:  req.ChannelType,
		Config:       req.Config,
		RoutingRules: req.RoutingRules,
		Enabled:      req.Enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	recordAudit(c, h.store, h.log, "channel.update", "alarm_channel", item.ID.String(), "Updated channel "+item.Name, map[string]any{
		"name":         item.Name,
		"channel_type": item.ChannelType,
		"config":       req.Config,
	})
	respondOK(c, item)
}

// Delete removes an alarm channel.
func (h *ChannelsHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel id", err))
		return
	}
	if err := h.store.Queries().DeleteAlarmChannel(c.Request.Context(), id); err != nil {
		respondErr(c, err)
		return
	}
	recordAudit(c, h.store, h.log, "channel.delete", "alarm_channel", id.String(), "Deleted channel", nil)
	respondOK(c, gin.H{"deleted": true})
}

// Reset enqueues a manual silence/reset command to aegis-dispatcher over RabbitMQ (preserving api/notifier isolation).
func (h *ChannelsHandler) Reset(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel id", err))
		return
	}
	ch, err := h.store.Queries().GetAlarmChannelByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if ch.ChannelType != "sound_light" {
		respondErr(c, domain.NewInvalidArgumentError("reset is only supported for sound_light channels", nil))
		return
	}
	if h.pub != nil {
		body, _ := json.Marshal(map[string]any{
			"action":     "reset_channel",
			"channel_id": ch.ID.String(),
		})
		if pubErr := h.pub.PublishAlarm(c.Request.Context(), body); pubErr != nil {
			respondErr(c, domain.NewInternalError("failed to enqueue channel reset", pubErr))
			return
		}
	}
	recordAudit(c, h.store, h.log, "channel.reset", "alarm_channel", ch.ID.String(), "Triggered manual reset on "+ch.Name, map[string]any{"channel_type": ch.ChannelType})
	respondOK(c, gin.H{"reset_enqueued": true, "channel_id": ch.ID.String()})
}

// Test sends a synthetic connectivity test event to webhook, wecom, dingtalk, or sound_light channels.
func (h *ChannelsHandler) Test(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel id", err))
		return
	}
	ch, err := h.store.Queries().GetAlarmChannelByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}

	var cfg channelTestConfig
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel config json", err))
		return
	}

	var targetRaw string
	var payload []byte
	method := http.MethodPost
	switch ch.ChannelType {
	case "webhook":
		targetRaw = cfg.URL
		if strings.TrimSpace(cfg.Method) != "" {
			method = strings.ToUpper(strings.TrimSpace(cfg.Method))
		}
		payload, _ = json.Marshal(map[string]any{
			"event":      "channel.test",
			"channel_id": ch.ID.String(),
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		})
	case "wecom":
		targetRaw = cfg.WebhookURL
		if targetRaw == "" {
			targetRaw = cfg.URL
		}
		payload, _ = json.Marshal(map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": fmt.Sprintf("### AegisVision 通道测试\n> 通道：%s\n> 状态：连通正常", ch.Name)},
		})
	case "dingtalk":
		targetRaw = cfg.WebhookURL
		if targetRaw == "" {
			targetRaw = cfg.URL
		}
		payload, _ = json.Marshal(map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"title": "AegisVision Test", "text": fmt.Sprintf("#### AegisVision 通道测试\n- 通道：%s", ch.Name)},
		})
	case "sound_light":
		targetRaw = cfg.TriggerURL
		if strings.TrimSpace(cfg.Method) != "" {
			method = strings.ToUpper(strings.TrimSpace(cfg.Method))
		}
		payload, _ = json.Marshal(map[string]any{"action": "test", "channel_id": ch.ID.String()})
	default:
		respondErr(c, domain.NewInvalidArgumentError("unsupported channel type", nil))
		return
	}

	parsedURL, valErr := validateHTTPURL(targetRaw)
	if valErr != nil {
		respondErr(c, valErr)
		return
	}
	if ch.ChannelType == "dingtalk" && cfg.Secret != "" {
		tsMs := time.Now().UTC().UnixMilli()
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		_, _ = fmt.Fprintf(mac, "%d\n%s", tsMs, cfg.Secret)
		q := parsedURL.Query()
		q.Set("timestamp", strconv.FormatInt(tsMs, 10))
		q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		parsedURL.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), method, parsedURL.String(), bytes.NewReader(payload))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel url", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.ChannelType == "webhook" {
		tsStr := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		req.Header.Set("X-Aegis-Event", "channel.test")
		req.Header.Set("X-Aegis-Timestamp", tsStr)
		if cfg.Secret != "" {
			mac := hmac.New(sha256.New, []byte(cfg.Secret))
			_, _ = mac.Write([]byte(tsStr))
			_, _ = mac.Write([]byte("."))
			_, _ = mac.Write(payload)
			req.Header.Set("X-Aegis-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := h.httpClient.Do(req)
	if err != nil {
		respondErr(c, domain.NewInternalError("channel test request failed", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	respondOK(c, gin.H{
		"status_code": resp.StatusCode,
		"latency_ms":  time.Since(start).Milliseconds(),
		"ok":          resp.StatusCode >= 200 && resp.StatusCode < 300,
	})
}
