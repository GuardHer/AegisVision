package api

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GuardHer/AegisVision/backend/internal/auth"
	"github.com/GuardHer/AegisVision/backend/internal/domain"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres/sqlcgen"
)

// AlarmsHandler serves the Alarm Center query, audit handling, and delivery retry endpoints.
type AlarmsHandler struct {
	store *postgres.Store
	pub   EventPublisher
	log   *slog.Logger
}

// NewAlarmsHandler constructs an AlarmsHandler.
func NewAlarmsHandler(store *postgres.Store, pub EventPublisher, log *slog.Logger) *AlarmsHandler {
	return &AlarmsHandler{store: store, pub: pub, log: log}
}

type handleAlarmRequest struct {
	HandleStatus string `json:"handle_status" binding:"required"`
	HandleNote   string `json:"handle_note"`
}

type alarmDispatchRetryMessage struct {
	AlarmID    string `json:"alarm_id"`
	DeliveryID string `json:"delivery_id"`
	ChannelID  string `json:"channel_id"`
	Manual     bool   `json:"manual"`
}

// List returns paginated alarms with optional camera_id, rule_mode, handle_status, and time filters.
func (h *AlarmsHandler) List(c *gin.Context) {
	page := parsePositiveInt(c.Query("page"), 1)
	pageSize := parsePositiveInt(c.Query("page_size"), 20)
	if pageSize > 200 {
		pageSize = 200
	}

	var camFilter uuid.NullUUID
	if rawCam := c.Query("camera_id"); rawCam != "" {
		id, err := uuid.Parse(rawCam)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid camera_id filter", err))
			return
		}
		camFilter = uuid.NullUUID{UUID: id, Valid: true}
	}

	ruleModeFilter := toNullableText(c.Query("rule_mode"))
	handleStatusFilter := toNullableText(c.Query("handle_status"))

	var startTime, endTime *time.Time
	if raw := c.Query("start_time"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("start_time must be RFC3339", err))
			return
		}
		startTime = &t
	}
	if raw := c.Query("end_time"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("end_time must be RFC3339", err))
			return
		}
		endTime = &t
	}

	ctx := c.Request.Context()
	items, err := h.store.Queries().ListAlarms(ctx, sqlcgen.ListAlarmsParams{
		CameraID:     camFilter,
		RuleMode:     ruleModeFilter,
		HandleStatus: handleStatusFilter,
		StartTime:    startTime,
		EndTime:      endTime,
		PageLimit:    int32(pageSize),             // #nosec G115 -- pageSize is bounded to <= 200
		PageOffset:   int32((page - 1) * pageSize), // #nosec G115 -- pagination offset
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	total, err := h.store.Queries().CountAlarms(ctx, sqlcgen.CountAlarmsParams{
		CameraID:     camFilter,
		RuleMode:     ruleModeFilter,
		HandleStatus: handleStatusFilter,
		StartTime:    startTime,
		EndTime:      endTime,
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	respondOK(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Get returns a single alarm record by ID.
func (h *AlarmsHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid alarm id", err))
		return
	}
	item, err := h.store.Queries().GetAlarmByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, item)
}

// Handle records operator/admin confirmation or false-positive audit metadata on an alarm.
func (h *AlarmsHandler) Handle(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid alarm id", err))
		return
	}
	var req handleAlarmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("handle_status is required", err))
		return
	}
	if req.HandleStatus != domain.HandleStatusConfirmed && req.HandleStatus != domain.HandleStatusFalsePositive {
		respondErr(c, domain.NewInvalidArgumentError("handle_status must be confirmed or false_positive", nil))
		return
	}

	var handledBy uuid.NullUUID
	if claims, ok := auth.CurrentClaims(c); ok {
		if uid, parseErr := uuid.Parse(claims.UserID); parseErr == nil {
			handledBy = uuid.NullUUID{UUID: uid, Valid: true}
		}
	}

	updated, err := h.store.Queries().HandleAlarm(c.Request.Context(), sqlcgen.HandleAlarmParams{
		ID:           id,
		HandleStatus: req.HandleStatus,
		HandledBy:    handledBy,
		HandleNote:   req.HandleNote,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, updated)
}

// ListDeliveries returns all channel delivery attempts associated with an alarm.
func (h *AlarmsHandler) ListDeliveries(c *gin.Context) {
	alarmID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid alarm id", err))
		return
	}
	items, err := h.store.Queries().ListDeliveriesByAlarm(c.Request.Context(), alarmID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// RetryDelivery resets a failed/dead-letter delivery to pending and re-enqueues it to RabbitMQ.
func (h *AlarmsHandler) RetryDelivery(c *gin.Context) {
	deliveryID, err := uuid.Parse(c.Param("delivery_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid delivery id", err))
		return
	}

	ctx := c.Request.Context()
	delivery, err := h.store.Queries().GetAlarmDeliveryByID(ctx, deliveryID)
	if err != nil {
		respondErr(c, err)
		return
	}

	updated, err := h.store.Queries().UpdateAlarmDeliveryResult(ctx, sqlcgen.UpdateAlarmDeliveryResultParams{
		ID:             delivery.ID,
		Status:         "pending",
		Attempts:       delivery.Attempts,
		LastStatusCode: delivery.LastStatusCode,
		LastLatencyMs:  delivery.LastLatencyMs,
		LastError:      "",
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	if h.pub != nil {
		payload, _ := json.Marshal(alarmDispatchRetryMessage{
			AlarmID:    delivery.AlarmID.String(),
			DeliveryID: delivery.ID.String(),
			ChannelID:  delivery.ChannelID.String(),
			Manual:     true,
		})
		if err := h.pub.PublishAlarm(ctx, payload); err != nil {
			respondErr(c, domain.NewInternalError("failed to enqueue delivery retry", err))
			return
		}
	}

	respondOK(c, updated)
}

func parsePositiveInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

var _ = pgtype.Text{}
