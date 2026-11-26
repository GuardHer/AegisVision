package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/GuardHer/AegisVision/backend/internal/domain"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres/sqlcgen"
)

// ChannelsHandler manages external notification channels (e.g., Webhook).
type ChannelsHandler struct {
	store      *postgres.Store
	httpClient *http.Client
}

// NewChannelsHandler constructs a ChannelsHandler.
func NewChannelsHandler(store *postgres.Store, httpClient *http.Client) *ChannelsHandler {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &ChannelsHandler{
		store:      store,
		httpClient: httpClient,
	}
}

type createChannelRequest struct {
	Name        string          `json:"name" binding:"required"`
	ChannelType string          `json:"channel_type" binding:"required"`
	Config      json.RawMessage `json:"config" binding:"required"`
	Enabled     *bool           `json:"enabled"`
}

type updateChannelRequest struct {
	Name        string          `json:"name" binding:"required"`
	ChannelType string          `json:"channel_type" binding:"required"`
	Config      json.RawMessage `json:"config" binding:"required"`
	Enabled     bool            `json:"enabled"`
}

type webhookChannelConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
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

// Create inserts a new alarm channel.
func (h *ChannelsHandler) Create(c *gin.Context) {
	var req createChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel payload", err))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	item, err := h.store.Queries().CreateAlarmChannel(c.Request.Context(), sqlcgen.CreateAlarmChannelParams{
		ID:          uuid.Must(uuid.NewV7()),
		Name:        req.Name,
		ChannelType: req.ChannelType,
		Config:      req.Config,
		Enabled:     enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
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

	item, err := h.store.Queries().UpdateAlarmChannel(c.Request.Context(), sqlcgen.UpdateAlarmChannelParams{
		ID:          id,
		Name:        req.Name,
		ChannelType: req.ChannelType,
		Config:      req.Config,
		Enabled:     req.Enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
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
	respondOK(c, gin.H{"deleted": true})
}

// Test sends a synthetic connectivity test event to the configured channel.
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
	if ch.ChannelType != "webhook" {
		respondErr(c, domain.NewInvalidArgumentError("test is only implemented for webhook channels", nil))
		return
	}

	var whCfg webhookChannelConfig
	if err := json.Unmarshal(ch.Config, &whCfg); err != nil || strings.TrimSpace(whCfg.URL) == "" {
		respondErr(c, domain.NewInvalidArgumentError("webhook channel missing valid url", err))
		return
	}
	method := whCfg.Method
	if method == "" {
		method = http.MethodPost
	}

	payload, _ := json.Marshal(map[string]any{
		"event":      "channel.test",
		"channel_id": ch.ID.String(),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	})

	req, err := http.NewRequestWithContext(c.Request.Context(), method, whCfg.URL, bytes.NewReader(payload))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid webhook url", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aegis-Event", "channel.test")
	for k, v := range whCfg.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := h.httpClient.Do(req)
	if err != nil {
		respondErr(c, domain.NewInternalError("webhook test request failed", err))
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
