package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/platform/storage"
)

// AlarmEvidenceReader retrieves alarm metadata to resolve S3 object keys.
type AlarmEvidenceReader interface {
	GetAlarmByID(ctx context.Context, id uuid.UUID) (sqlcgen.Alarm, error)
}

// EvidenceHandler proxies S3/local alarm evidence images and Range-capable MP4 videos.
type EvidenceHandler struct {
	alarms    AlarmEvidenceReader
	store     storage.ObjectStorage
	tokens    *auth.TokenManager
	blacklist auth.TokenBlacklist
	signer    *auth.EvidenceSigner
}

// NewEvidenceHandler creates an EvidenceHandler.
func NewEvidenceHandler(
	alarms AlarmEvidenceReader,
	store storage.ObjectStorage,
	tokens *auth.TokenManager,
	blacklist auth.TokenBlacklist,
	signer *auth.EvidenceSigner,
) *EvidenceHandler {
	return &EvidenceHandler{
		alarms:    alarms,
		store:     store,
		tokens:    tokens,
		blacklist: blacklist,
		signer:    signer,
	}
}

// ServeImage streams the alarm's snapshot JPEG.
func (h *EvidenceHandler) ServeImage(c *gin.Context) {
	h.serve(c, "image")
}

// ServeVideo streams the alarm's MP4 clip with full HTTP Range (206 Partial Content) support.
func (h *EvidenceHandler) ServeVideo(c *gin.Context) {
	h.serve(c, "video")
}

func (h *EvidenceHandler) serve(c *gin.Context, kind string) {
	alarmIDStr := c.Param("alarm_id")
	if !h.isAuthorized(c, alarmIDStr, kind) {
		respondErr(c, domain.NewUnauthorizedError("valid token or signature required", nil))
		return
	}

	alarmID, err := uuid.Parse(alarmIDStr)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid alarm_id", err))
		return
	}

	alarm, err := h.alarms.GetAlarmByID(c.Request.Context(), alarmID)
	if err != nil {
		respondErr(c, err)
		return
	}

	var objectKey string
	if kind == "image" {
		objectKey = alarm.ImageObjectKey
	} else {
		objectKey = alarm.VideoObjectKey
	}
	if objectKey == "" {
		respondErr(c, domain.NewNotFoundError("evidence not available", nil))
		return
	}

	rangeHeader := c.GetHeader("Range")
	rc, meta, err := h.store.GetObject(c.Request.Context(), objectKey, rangeHeader)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondErr(c, domain.NewNotFoundError("evidence object not found", err))
			return
		}
		if errors.Is(err, storage.ErrInvalidRange) {
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		respondErr(c, domain.NewInternalError("failed to load evidence object", err))
		return
	}
	defer func() { _ = rc.Close() }()

	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Type", meta.ContentType)
	if meta.ContentLength >= 0 {
		c.Header("Content-Length", strconv.FormatInt(meta.ContentLength, 10))
	}

	status := http.StatusOK
	if meta.Partial && meta.ContentRange != "" {
		c.Header("Content-Range", meta.ContentRange)
		status = http.StatusPartialContent
	}
	c.Status(status)
	_, _ = io.Copy(c.Writer, rc)
}

func (h *EvidenceHandler) isAuthorized(c *gin.Context, alarmID, kind string) bool {
	// 1. Check HMAC-signed URL query parameters (?exp=...&sig=...)
	exp := c.Query("exp")
	sig := c.Query("sig")
	if exp != "" && sig != "" && h.signer != nil {
		if h.signer.VerifyEvidenceSignature(alarmID, kind, exp, sig, time.Now().UTC()) {
			return true
		}
	}

	// 2. Fall back to JWT Bearer token (or ?token= for <img>/<video> browser elements)
	rawToken := auth.ExtractBearerToken(c.GetHeader("Authorization"))
	if rawToken == "" {
		rawToken = c.Query("token")
	}
	if rawToken == "" || h.tokens == nil {
		return false
	}

	claims, err := h.tokens.ParseAccessToken(rawToken)
	if err != nil {
		return false
	}
	if h.blacklist != nil && claims.ID != "" {
		revoked, blErr := h.blacklist.IsTokenBlacklisted(c.Request.Context(), claims.ID)
		if blErr != nil || revoked {
			return false
		}
	}
	return true
}
