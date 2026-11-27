package api

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/platform/storage"
)

const maxAlarmExportRows = 500

// AlarmsHandler serves the Alarm Center query, batch audit handling, streaming export, and delivery retry endpoints.
type AlarmsHandler struct {
	store   *postgres.Store
	storage storage.ObjectStorage
	pub     EventPublisher
	log     *slog.Logger
}

// NewAlarmsHandler constructs an AlarmsHandler.
func NewAlarmsHandler(store *postgres.Store, objStore storage.ObjectStorage, pub EventPublisher, log *slog.Logger) *AlarmsHandler {
	return &AlarmsHandler{store: store, storage: objStore, pub: pub, log: log}
}

type handleAlarmRequest struct {
	HandleStatus        string `json:"handle_status" binding:"required"`
	FalsePositiveReason string `json:"false_positive_reason"`
	HandleNote          string `json:"handle_note"`
}

type batchHandleAlarmsRequest struct {
	AlarmIDs            []string `json:"alarm_ids" binding:"required"`
	HandleStatus        string   `json:"handle_status" binding:"required"`
	FalsePositiveReason string   `json:"false_positive_reason"`
	HandleNote          string   `json:"handle_note"`
}

type exportAlarmsRequest struct {
	Format       string `json:"format"`
	ZoneID       string `json:"zone_id"`
	CameraID     string `json:"camera_id"`
	RuleMode     string `json:"rule_mode"`
	Severity     string `json:"severity"`
	HandleStatus string `json:"handle_status"`
	Keyword      string `json:"keyword"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Limit        int32  `json:"limit"`
}

type alarmDispatchRetryMessage struct {
	AlarmID    string `json:"alarm_id"`
	DeliveryID string `json:"delivery_id"`
	ChannelID  string `json:"channel_id"`
	Manual     bool   `json:"manual"`
}

func isValidHandleStatus(status string) bool {
	return status == domain.HandleStatusConfirmed ||
		status == domain.HandleStatusFalsePositive ||
		status == domain.HandleStatusIgnored
}

func currentHandledBy(c *gin.Context) uuid.NullUUID {
	if claims, ok := auth.CurrentClaims(c); ok {
		if uid, err := uuid.Parse(claims.UserID); err == nil {
			return uuid.NullUUID{UUID: uid, Valid: true}
		}
	}
	return uuid.NullUUID{}
}

// List returns paginated alarms with optional zone_id, camera_id, rule_mode, severity, handle_status, keyword, and time filters.
func (h *AlarmsHandler) List(c *gin.Context) {
	page := parsePositiveInt(c.Query("page"), 1)
	pageSize := parsePositiveInt(c.Query("page_size"), 20)
	if pageSize > 200 {
		pageSize = 200
	}

	ctx := c.Request.Context()
	var camFilter uuid.NullUUID
	if rawCam := strings.TrimSpace(c.Query("camera_id")); rawCam != "" {
		id, err := uuid.Parse(rawCam)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid camera_id filter", err))
			return
		}
		camFilter = uuid.NullUUID{UUID: id, Valid: true}
	}

	var zoneIDs []uuid.UUID
	if rawZone := strings.TrimSpace(c.Query("zone_id")); rawZone != "" {
		zid, err := uuid.Parse(rawZone)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid zone_id filter", err))
			return
		}
		desc, descErr := collectZoneDescendants(ctx, h.store.Queries(), zid)
		if descErr != nil {
			respondErr(c, descErr)
			return
		}
		zoneIDs = desc
	}

	ruleModeFilter := toNullableText(c.Query("rule_mode"))
	severityFilter := toNullableText(c.Query("severity"))
	handleStatusFilter := toNullableText(c.Query("handle_status"))
	keywordFilter := toNullableText(c.Query("keyword"))

	var startTime, endTime *time.Time
	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("start_time must be RFC3339", err))
			return
		}
		startTime = &t
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("end_time must be RFC3339", err))
			return
		}
		endTime = &t
	}

	items, err := h.store.Queries().ListAlarms(ctx, sqlcgen.ListAlarmsParams{
		CameraID:     camFilter,
		ZoneIds:      zoneIDs,
		RuleMode:     ruleModeFilter,
		Severity:     severityFilter,
		HandleStatus: handleStatusFilter,
		Keyword:      keywordFilter,
		StartTime:    startTime,
		EndTime:      endTime,
		PageLimit:    int32(pageSize),              // #nosec G115 -- pageSize is bounded to <= 200
		PageOffset:   int32((page - 1) * pageSize), // #nosec G115 -- pagination offset
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	total, err := h.store.Queries().CountAlarms(ctx, sqlcgen.CountAlarmsParams{
		CameraID:     camFilter,
		ZoneIds:      zoneIDs,
		RuleMode:     ruleModeFilter,
		Severity:     severityFilter,
		HandleStatus: handleStatusFilter,
		Keyword:      keywordFilter,
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

// Handle records operator/admin confirmation, false-positive (with reason), or ignored audit metadata on an alarm.
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
	if !isValidHandleStatus(req.HandleStatus) {
		respondErr(c, domain.NewInvalidArgumentError("handle_status must be confirmed, false_positive, or ignored", nil))
		return
	}

	fpReason := strings.TrimSpace(req.FalsePositiveReason)
	if req.HandleStatus != domain.HandleStatusFalsePositive {
		fpReason = ""
	}

	updated, err := h.store.Queries().HandleAlarm(c.Request.Context(), sqlcgen.HandleAlarmParams{
		ID:                  id,
		HandleStatus:        req.HandleStatus,
		HandledBy:           currentHandledBy(c),
		HandleNote:          req.HandleNote,
		FalsePositiveReason: fpReason,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, updated)
}

// BatchHandle atomically applies handle_status across multiple alarms.
func (h *AlarmsHandler) BatchHandle(c *gin.Context) {
	var req batchHandleAlarmsRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.AlarmIDs) == 0 {
		respondErr(c, domain.NewInvalidArgumentError("alarm_ids and handle_status are required", err))
		return
	}
	if !isValidHandleStatus(req.HandleStatus) {
		respondErr(c, domain.NewInvalidArgumentError("handle_status must be confirmed, false_positive, or ignored", nil))
		return
	}
	ids, err := parseUUIDSlice(req.AlarmIDs)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid alarm_ids", err))
		return
	}

	fpReason := strings.TrimSpace(req.FalsePositiveReason)
	if req.HandleStatus != domain.HandleStatusFalsePositive {
		fpReason = ""
	}

	affected, err := h.store.Queries().BatchHandleAlarms(c.Request.Context(), sqlcgen.BatchHandleAlarmsParams{
		HandleStatus:        req.HandleStatus,
		HandledBy:           currentHandledBy(c),
		HandleNote:          req.HandleNote,
		FalsePositiveReason: fpReason,
		Ids:                 ids,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"updated_count": affected})
}

// Export streams filtered alarms (hard-capped at 500 rows) as UTF-8 BOM CSV or an evidence ZIP bundle.
func (h *AlarmsHandler) Export(c *gin.Context) {
	var req exportAlarmsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid export payload", err))
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "zip" {
		respondErr(c, domain.NewInvalidArgumentError("format must be csv or zip", nil))
		return
	}

	limit := req.Limit
	if limit <= 0 || limit > maxAlarmExportRows {
		limit = maxAlarmExportRows
	}

	ctx := c.Request.Context()
	var camFilter uuid.NullUUID
	if rawCam := strings.TrimSpace(req.CameraID); rawCam != "" {
		id, err := uuid.Parse(rawCam)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid camera_id", err))
			return
		}
		camFilter = uuid.NullUUID{UUID: id, Valid: true}
	}
	var zoneIDs []uuid.UUID
	if rawZone := strings.TrimSpace(req.ZoneID); rawZone != "" {
		zid, err := uuid.Parse(rawZone)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid zone_id", err))
			return
		}
		desc, descErr := collectZoneDescendants(ctx, h.store.Queries(), zid)
		if descErr != nil {
			respondErr(c, descErr)
			return
		}
		zoneIDs = desc
	}

	var startTime, endTime *time.Time
	if raw := strings.TrimSpace(req.StartTime); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("start_time must be RFC3339", err))
			return
		}
		startTime = &t
	}
	if raw := strings.TrimSpace(req.EndTime); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("end_time must be RFC3339", err))
			return
		}
		endTime = &t
	}

	rows, err := h.store.Queries().ListAlarmsForExport(ctx, sqlcgen.ListAlarmsForExportParams{
		CameraID:     camFilter,
		ZoneIds:      zoneIDs,
		RuleMode:     toNullableText(req.RuleMode),
		Severity:     toNullableText(req.Severity),
		HandleStatus: toNullableText(req.HandleStatus),
		Keyword:      toNullableText(req.Keyword),
		StartTime:    startTime,
		EndTime:      endTime,
		MaxRows:      limit,
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	tsTag := time.Now().UTC().Format("20060102-150405")
	if format == "csv" {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="aegis-alarms-%s.csv"`, tsTag))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
		w := csv.NewWriter(c.Writer)
		_ = w.Write([]string{
			"alarm_id", "triggered_at", "zone_name", "camera_name", "rule_mode",
			"severity", "title", "detected_count", "handle_status", "false_positive_reason", "handle_note",
		})
		for _, r := range rows {
			_ = w.Write([]string{
				r.ID.String(),
				r.TriggeredAt.UTC().Format(time.RFC3339),
				r.ZoneName,
				r.CameraName,
				r.RuleMode,
				r.Severity,
				r.Title,
				strconv.Itoa(int(r.DetectedCount)),
				r.HandleStatus,
				r.FalsePositiveReason,
				r.HandleNote,
			})
		}
		w.Flush()
		return
	}

	// Streaming ZIP archive: metadata.json + images/<alarm_id>.jpg
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="aegis-evidence-%s.zip"`, tsTag))
	c.Status(http.StatusOK)

	zw := zip.NewWriter(c.Writer)
	defer func() { _ = zw.Close() }()

	type exportMetaItem struct {
		AlarmID             string          `json:"alarm_id"`
		TriggeredAt         string          `json:"triggered_at"`
		ZoneName            string          `json:"zone_name"`
		CameraName          string          `json:"camera_name"`
		RuleMode            string          `json:"rule_mode"`
		Severity            string          `json:"severity"`
		Title               string          `json:"title"`
		HandleStatus        string          `json:"handle_status"`
		FalsePositiveReason string          `json:"false_positive_reason,omitempty"`
		HandleNote          string          `json:"handle_note,omitempty"`
		ImageFile           string          `json:"image_file,omitempty"`
		Detections          json.RawMessage `json:"detections"`
	}

	metaItems := make([]exportMetaItem, 0, len(rows))
	for _, r := range rows {
		imgFile := ""
		if r.ImageObjectKey != "" && h.storage != nil {
			rc, _, getErr := h.storage.GetObject(ctx, r.ImageObjectKey, "")
			if getErr == nil {
				imgName := fmt.Sprintf("images/%s.jpg", r.ID.String())
				if fw, zErr := zw.Create(imgName); zErr == nil {
					_, _ = io.Copy(fw, rc)
					imgFile = imgName
				}
				_ = rc.Close()
			}
		}
		dets := json.RawMessage(r.DetectionsSnapshot)
		if len(dets) == 0 {
			dets = json.RawMessage(`[]`)
		}
		metaItems = append(metaItems, exportMetaItem{
			AlarmID:             r.ID.String(),
			TriggeredAt:         r.TriggeredAt.UTC().Format(time.RFC3339),
			ZoneName:            r.ZoneName,
			CameraName:          r.CameraName,
			RuleMode:            r.RuleMode,
			Severity:            r.Severity,
			Title:               r.Title,
			HandleStatus:        r.HandleStatus,
			FalsePositiveReason: r.FalsePositiveReason,
			HandleNote:          r.HandleNote,
			ImageFile:           imgFile,
			Detections:          dets,
		})
	}

	if mw, err := zw.Create("metadata.json"); err == nil {
		enc := json.NewEncoder(mw)
		enc.SetIndent("", "  ")
		_ = enc.Encode(metaItems)
	}
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
