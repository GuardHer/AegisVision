package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/GuardHer/AegisVision/backend/internal/auth"
	"github.com/GuardHer/AegisVision/backend/internal/domain"
	"github.com/GuardHer/AegisVision/backend/internal/platform/mediamtx"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres"
	platformredis "github.com/GuardHer/AegisVision/backend/internal/platform/redis"
	"github.com/GuardHer/AegisVision/backend/internal/platform/storage"
	"github.com/GuardHer/AegisVision/backend/internal/platform/telemetry"
)

// CacheStore combines Redis operations needed by aegis-api.
type CacheStore interface {
	AuthCache
	StreamTokenReader
	SetStreamToken(ctx context.Context, kind, token string, payload platformredis.StreamTokenPayload, ttl time.Duration) error
}

// EventPublisher abstracts RabbitMQ publishing needed by aegis-api.
type EventPublisher interface {
	PublishConfigEvent(ctx context.Context, body []byte) error
	PublishAlarm(ctx context.Context, body []byte) error
}

// RouterDeps aggregates explicit dependencies for constructing the aegis-api Gin engine.
type RouterDeps struct {
	Store           *postgres.Store
	Cache           CacheStore
	Publisher       EventPublisher
	Storage         storage.ObjectStorage
	MediaMTX        *mediamtx.Client
	Tokens          *auth.TokenManager
	Signer          *auth.EvidenceSigner
	Health          *telemetry.Health
	Logger          *slog.Logger
	RefreshTokenTTL time.Duration
}

type configEventPayload struct {
	Type      string    `json:"type"`
	Resource  string    `json:"resource"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
}

func emitConfigEvent(ctx context.Context, pub EventPublisher, log *slog.Logger, resource, eventType, id string) {
	if pub == nil {
		return
	}
	raw, err := json.Marshal(configEventPayload{
		Type:      eventType,
		Resource:  resource,
		ID:        id,
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		return
	}
	if err := pub.PublishConfigEvent(ctx, raw); err != nil && log != nil {
		log.Warn("failed to publish config event", "resource", resource, "id", id, "err", err)
	}
}

// NewRouter constructs the complete aegis-api HTTP router with RBAC and internal callbacks.
func NewRouter(deps RouterDeps) *gin.Engine {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLoggerMiddleware(deps.Logger))

	// Probes & Prometheus metrics
	if deps.Health != nil {
		h := deps.Health.Handler()
		r.GET("/healthz", gin.WrapH(h))
		r.GET("/readyz", gin.WrapH(h))
		r.GET("/metrics", gin.WrapH(h))
	}

	// Internal MediaMTX external authentication callback (not exposed by Nginx)
	if deps.Cache != nil {
		r.POST("/internal/mediamtx/auth", NewMediaMTXAuthHandler(deps.Cache))
	}

	authH := NewAuthHandler(deps.Store, deps.Tokens, deps.Cache, deps.RefreshTokenTTL)
	usersH := NewUsersHandler(deps.Store)
	algosH := NewAlgorithmsHandler(deps.Store, deps.Publisher, deps.Logger)
	camsH := NewCamerasHandler(deps.Store, deps.Cache, deps.MediaMTX, deps.Publisher, deps.Logger)
	rulesH := NewRulesHandler(deps.Store, deps.Publisher, deps.Logger)
	nodesH := NewNodesTasksHandler(deps.Store, deps.Publisher, deps.Logger)
	alarmsH := NewAlarmsHandler(deps.Store, deps.Publisher, deps.Logger)
	channelsH := NewChannelsHandler(deps.Store, nil)
	evidenceH := NewEvidenceHandler(deps.Store.Queries(), deps.Storage, deps.Tokens, deps.Cache, deps.Signer)

	v1 := r.Group("/api/v1")

	// Public auth endpoints
	v1.POST("/auth/login", authH.Login)
	v1.POST("/auth/refresh", authH.Refresh)

	// Evidence proxy supports either JWT Bearer or HMAC signed URL (?exp=&sig=)
	v1.GET("/evidence/:alarm_id/image", evidenceH.ServeImage)
	v1.GET("/evidence/:alarm_id/video", evidenceH.ServeVideo)

	// Authenticated routes
	authed := v1.Group("")
	authed.Use(auth.AuthMiddleware(deps.Tokens, deps.Cache))

	allRoles := auth.RequireRoles(domain.RoleAdmin, domain.RoleOperator, domain.RoleViewer)
	opRoles := auth.RequireRoles(domain.RoleAdmin, domain.RoleOperator)
	adminOnly := auth.RequireRoles(domain.RoleAdmin)

	// Self auth
	authed.POST("/auth/logout", allRoles, authH.Logout)
	authed.GET("/auth/me", allRoles, authH.Me)

	// Users (admin only)
	authed.GET("/users", adminOnly, usersH.List)
	authed.POST("/users", adminOnly, usersH.Create)
	authed.PUT("/users/:id", adminOnly, usersH.Update)
	authed.DELETE("/users/:id", adminOnly, usersH.Delete)
	authed.POST("/users/:id/reset-password", adminOnly, usersH.ResetPassword)

	// Algorithms & versions
	authed.GET("/algorithms", allRoles, algosH.ListAlgorithms)
	authed.POST("/algorithms", adminOnly, algosH.CreateAlgorithm)
	authed.PUT("/algorithms/:id", adminOnly, algosH.UpdateAlgorithm)
	authed.DELETE("/algorithms/:id", adminOnly, algosH.DeleteAlgorithm)
	authed.GET("/algorithms/:id/versions", allRoles, algosH.ListVersions)
	authed.POST("/algorithms/:id/versions", adminOnly, algosH.CreateVersion)
	authed.PUT("/algorithm-versions/:version_id", adminOnly, algosH.UpdateVersion)

	// Cameras, stream token, ROIs, rules
	authed.GET("/cameras", allRoles, camsH.ListCameras)
	authed.POST("/cameras", adminOnly, camsH.CreateCamera)
	authed.GET("/cameras/:id", allRoles, camsH.GetCamera)
	authed.PUT("/cameras/:id", adminOnly, camsH.UpdateCamera)
	authed.DELETE("/cameras/:id", adminOnly, camsH.DeleteCamera)
	authed.POST("/cameras/:id/stream-token", allRoles, camsH.CreateStreamToken)

	authed.GET("/cameras/:id/rois", allRoles, camsH.ListROIs)
	authed.POST("/cameras/:id/rois", adminOnly, camsH.CreateROI)
	authed.PUT("/rois/:roi_id", adminOnly, camsH.UpdateROI)
	authed.DELETE("/rois/:roi_id", adminOnly, camsH.DeleteROI)

	authed.GET("/cameras/:id/rules", allRoles, rulesH.ListByCamera)
	authed.POST("/cameras/:id/rules", adminOnly, rulesH.Create)
	authed.PUT("/rules/:rule_id", adminOnly, rulesH.Update)
	authed.DELETE("/rules/:rule_id", adminOnly, rulesH.Delete)

	// Compute nodes & inference tasks (admin only)
	authed.GET("/nodes", adminOnly, nodesH.ListNodes)
	authed.GET("/nodes/:node_id", adminOnly, nodesH.GetNode)
	authed.PUT("/nodes/:node_id/status", adminOnly, nodesH.UpdateNodeStatus)
	authed.GET("/tasks", adminOnly, nodesH.ListTasks)
	authed.POST("/tasks/:id/reschedule", adminOnly, nodesH.RescheduleTask)

	// Alarms & deliveries
	authed.GET("/alarms", allRoles, alarmsH.List)
	authed.GET("/alarms/:id", allRoles, alarmsH.Get)
	authed.POST("/alarms/:id/handle", opRoles, alarmsH.Handle)
	authed.GET("/alarms/:id/deliveries", allRoles, alarmsH.ListDeliveries)
	authed.POST("/deliveries/:delivery_id/retry", opRoles, alarmsH.RetryDelivery)

	// Alarm channels (admin only)
	authed.GET("/channels", adminOnly, channelsH.List)
	authed.POST("/channels", adminOnly, channelsH.Create)
	authed.PUT("/channels/:id", adminOnly, channelsH.Update)
	authed.DELETE("/channels/:id", adminOnly, channelsH.Delete)
	authed.POST("/channels/:id/test", adminOnly, channelsH.Test)

	return r
}

func requestLoggerMiddleware(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		reqID := c.GetHeader("X-Request-Id")
		if reqID == "" {
			reqID = uuid.Must(uuid.NewV7()).String()
		}
		c.Header("X-Request-Id", reqID)

		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		log.Log(c.Request.Context(), level, "http request",
			"request_id", reqID,
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	}
}
