package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	// Embed IANA timezone database for settings timezone validation.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/platform/storage"
)

type cachePinger interface {
	Ping(ctx context.Context) error
}

// GovernanceHandler serves audit logs, platform global settings, and system health diagnostics.
type GovernanceHandler struct {
	store    *postgres.Store
	cache    CacheStore
	mediamtx *mediamtx.Client
	storage  storage.ObjectStorage
	log      *slog.Logger
}

// NewGovernanceHandler constructs a GovernanceHandler.
func NewGovernanceHandler(
	store *postgres.Store,
	cache CacheStore,
	mtx *mediamtx.Client,
	objStore storage.ObjectStorage,
	log *slog.Logger,
) *GovernanceHandler {
	return &GovernanceHandler{
		store:    store,
		cache:    cache,
		mediamtx: mtx,
		storage:  objStore,
		log:      log,
	}
}

// PlatformSettings holds global enterprise console configuration persisted in system_settings.
type PlatformSettings struct {
	PlatformTitle     string `json:"platform_title"`
	DefaultAlarmChime bool   `json:"default_alarm_chime"`
	PopupMinSeverity  string `json:"popup_min_severity"`
	MaxExportRows     int32  `json:"max_export_rows"`
	DefaultTimezone   string `json:"default_timezone"`
}

func defaultPlatformSettings() PlatformSettings {
	return PlatformSettings{
		PlatformTitle:     "AegisVision EHS 智能视频监控平台",
		DefaultAlarmChime: true,
		PopupMinSeverity:  domain.SeverityWarning,
		MaxExportRows:     defaultDatasetExportRows,
		DefaultTimezone:   "Asia/Shanghai",
	}
}

// ListAuditLogs returns paginated, filterable operation audit logs (admin only).
func (h *GovernanceHandler) ListAuditLogs(c *gin.Context) {
	ctx := c.Request.Context()
	page, pageSize := parsePagination(c, 1, 20, 100)

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

	actorFilter := toNullableText(c.Query("actor_username"))
	resFilter := toNullableText(c.Query("resource_type"))
	actFilter := toNullableText(c.Query("action"))

	items, err := h.store.Queries().ListAuditLogs(ctx, sqlcgen.ListAuditLogsParams{
		ActorUsername: actorFilter,
		ResourceType:  resFilter,
		Action:        actFilter,
		StartTime:     startTime,
		EndTime:       endTime,
		PageLimit:     int32(pageSize),              // #nosec G115 -- bounded pagination
		PageOffset:    int32((page - 1) * pageSize), // #nosec G115 -- bounded pagination
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	total, err := h.store.Queries().CountAuditLogs(ctx, sqlcgen.CountAuditLogsParams{
		ActorUsername: actorFilter,
		ResourceType:  resFilter,
		Action:        actFilter,
		StartTime:     startTime,
		EndTime:       endTime,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	if items == nil {
		items = []sqlcgen.AuditLog{}
	}
	respondOK(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetSettings returns the merged global platform settings.
func (h *GovernanceHandler) GetSettings(c *gin.Context) {
	cfg := defaultPlatformSettings()
	row, err := h.store.Queries().GetSystemSettingByKey(c.Request.Context(), "platform")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		respondErr(c, err)
		return
	}
	if err == nil && len(row.Value) > 0 {
		_ = json.Unmarshal(row.Value, &cfg)
	}
	respondOK(c, cfg)
}

// UpdateSettings validates and persists global platform settings (admin only).
func (h *GovernanceHandler) UpdateSettings(c *gin.Context) {
	var req PlatformSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid settings payload", err))
		return
	}
	req.PlatformTitle = strings.TrimSpace(req.PlatformTitle)
	if req.PlatformTitle == "" {
		req.PlatformTitle = defaultPlatformSettings().PlatformTitle
	}
	req.PopupMinSeverity = strings.ToLower(strings.TrimSpace(req.PopupMinSeverity))
	if req.PopupMinSeverity == "" {
		req.PopupMinSeverity = domain.SeverityWarning
	}
	if req.PopupMinSeverity != domain.SeverityInfo && req.PopupMinSeverity != domain.SeverityWarning && req.PopupMinSeverity != domain.SeverityCritical {
		respondErr(c, domain.NewInvalidArgumentError("popup_min_severity must be info, warning, or critical", nil))
		return
	}
	if req.MaxExportRows <= 0 {
		req.MaxExportRows = defaultDatasetExportRows
	}
	if req.MaxExportRows < 10 || req.MaxExportRows > hardMaxDatasetExportRows {
		respondErr(c, domain.NewInvalidArgumentError("max_export_rows must be between 10 and 2000", nil))
		return
	}
	req.DefaultTimezone = strings.TrimSpace(req.DefaultTimezone)
	if req.DefaultTimezone == "" {
		req.DefaultTimezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(req.DefaultTimezone); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid default_timezone", err))
		return
	}

	raw, err := json.Marshal(req)
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to marshal settings", err))
		return
	}
	var updatedBy uuid.NullUUID
	if claims, ok := auth.CurrentClaims(c); ok && claims != nil {
		if uid, pErr := uuid.Parse(claims.UserID); pErr == nil {
			updatedBy = uuid.NullUUID{UUID: uid, Valid: true}
		}
	}
	if _, err := h.store.Queries().UpsertSystemSetting(c.Request.Context(), sqlcgen.UpsertSystemSettingParams{
		Key:       "platform",
		Value:     raw,
		UpdatedBy: updatedBy,
	}); err != nil {
		respondErr(c, err)
		return
	}

	recordAudit(c, h.store, h.log, "settings.update", "system_settings", "platform", "Updated global platform settings", map[string]any{
		"platform_title":      req.PlatformTitle,
		"default_alarm_chime": req.DefaultAlarmChime,
		"popup_min_severity":  req.PopupMinSeverity,
		"max_export_rows":     req.MaxExportRows,
		"default_timezone":    req.DefaultTimezone,
	})
	respondOK(c, req)
}

// SystemComponentHealth reports a single dependency's live status.
type SystemComponentHealth struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Latency int64  `json:"latency_ms"`
}

// SystemHealth reports live middleware connectivity and cluster capacity (admin only).
func (h *GovernanceHandler) SystemHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	components := make([]SystemComponentHealth, 0, 4)

	// 1. PostgreSQL
	pgStart := time.Now()
	if err := h.store.Ping(ctx); err != nil {
		components = append(components, SystemComponentHealth{Name: "postgresql", Status: "degraded", Detail: "unreachable", Latency: time.Since(pgStart).Milliseconds()})
	} else {
		components = append(components, SystemComponentHealth{Name: "postgresql", Status: "healthy", Detail: "connected", Latency: time.Since(pgStart).Milliseconds()})
	}

	// 2. Redis
	if pinger, ok := h.cache.(cachePinger); ok && pinger != nil {
		rStart := time.Now()
		if err := pinger.Ping(ctx); err != nil {
			components = append(components, SystemComponentHealth{Name: "redis", Status: "degraded", Detail: "unreachable", Latency: time.Since(rStart).Milliseconds()})
		} else {
			components = append(components, SystemComponentHealth{Name: "redis", Status: "healthy", Detail: "connected", Latency: time.Since(rStart).Milliseconds()})
		}
	} else {
		components = append(components, SystemComponentHealth{Name: "redis", Status: "not_configured", Detail: "cache disabled"})
	}

	// 3. MediaMTX
	if h.mediamtx != nil {
		mStart := time.Now()
		if err := h.mediamtx.Ping(ctx); err != nil {
			components = append(components, SystemComponentHealth{Name: "mediamtx", Status: "degraded", Detail: "unreachable", Latency: time.Since(mStart).Milliseconds()})
		} else {
			components = append(components, SystemComponentHealth{Name: "mediamtx", Status: "healthy", Detail: "connected", Latency: time.Since(mStart).Milliseconds()})
		}
	} else {
		components = append(components, SystemComponentHealth{Name: "mediamtx", Status: "not_configured", Detail: "streaming client disabled"})
	}

	// 4. Object Storage
	if h.storage != nil {
		components = append(components, SystemComponentHealth{Name: "storage", Status: "healthy", Detail: "configured"})
	} else {
		components = append(components, SystemComponentHealth{Name: "storage", Status: "not_configured", Detail: "storage disabled"})
	}

	nodeHealth, _ := h.store.Queries().GetDashboardNodeHealth(ctx)
	camHealth, _ := h.store.Queries().GetDashboardCameraHealth(ctx, nil)

	respondOK(c, gin.H{
		"checked_at": time.Now().UTC(),
		"components": components,
		"cluster": gin.H{
			"nodes_online":   nodeHealth.OnlineNodes,
			"nodes_total":    nodeHealth.TotalNodes,
			"running_tasks":  nodeHealth.RunningTasks,
			"cameras_online": camHealth.OnlineCount,
			"cameras_total":  camHealth.TotalCount,
		},
	})
}
