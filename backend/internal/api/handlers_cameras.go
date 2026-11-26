package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	platformredis "aegisvision/backend/internal/platform/redis"
)

const streamPlayTokenTTL = 60 * time.Second

// PointInput represents a normalized [0.0, 1.0] vertex of a camera ROI polygon.
type PointInput struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ValidateROIPoints enforces normalized [0.0, 1.0] coordinates and minimum polygon vertex count.
func ValidateROIPoints(roiType string, points []PointInput) error {
	switch roiType {
	case domain.ROITypeFullFrame:
		return nil
	case domain.ROITypePolygon:
		if len(points) < 3 {
			return domain.NewInvalidArgumentError("polygon ROI must contain at least 3 points", nil)
		}
		for i, pt := range points {
			if pt.X < 0.0 || pt.X > 1.0 || pt.Y < 0.0 || pt.Y > 1.0 {
				return domain.NewInvalidArgumentError(
					fmt.Sprintf("point[%d] (%v, %v) must be normalized within [0.0, 1.0]", i, pt.X, pt.Y),
					nil,
				)
			}
		}
		return nil
	default:
		return domain.NewInvalidArgumentError("roi_type must be polygon or full_frame", nil)
	}
}

// CamerasHandler manages cameras, short-lived stream play tokens, and camera ROIs.
type CamerasHandler struct {
	store    *postgres.Store
	cache    CacheStore
	mediamtx *mediamtx.Client
	pub      EventPublisher
	log      *slog.Logger
}

// NewCamerasHandler constructs a CamerasHandler.
func NewCamerasHandler(store *postgres.Store, cache CacheStore, mtx *mediamtx.Client, pub EventPublisher, log *slog.Logger) *CamerasHandler {
	return &CamerasHandler{
		store:    store,
		cache:    cache,
		mediamtx: mtx,
		pub:      pub,
		log:      log,
	}
}

type createCameraRequest struct {
	Name         string `json:"name" binding:"required"`
	Location     string `json:"location"`
	RtspURL      string `json:"rtsp_url" binding:"required"`
	SubStreamURL string `json:"sub_stream_url"`
	StreamPath   string `json:"stream_path"`
	CodecHint    string `json:"codec_hint"`
	Enabled      *bool  `json:"enabled"`
}

type updateCameraRequest struct {
	Name         string `json:"name" binding:"required"`
	Location     string `json:"location"`
	RtspURL      string `json:"rtsp_url" binding:"required"`
	SubStreamURL string `json:"sub_stream_url"`
	CodecHint    string `json:"codec_hint"`
	Enabled      bool   `json:"enabled"`
}

type createROIRequest struct {
	Name    string       `json:"name" binding:"required"`
	ROIType string       `json:"roi_type" binding:"required"`
	Points  []PointInput `json:"points"`
	Enabled *bool        `json:"enabled"`
}

type updateROIRequest struct {
	Name    string       `json:"name" binding:"required"`
	ROIType string       `json:"roi_type" binding:"required"`
	Points  []PointInput `json:"points"`
	Enabled bool         `json:"enabled"`
}

// ListCameras returns all configured cameras.
func (h *CamerasHandler) ListCameras(c *gin.Context) {
	items, err := h.store.Queries().ListCameras(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// GetCamera returns a single camera by ID.
func (h *CamerasHandler) GetCamera(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	cam, err := h.store.Queries().GetCameraByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, cam)
}

// CreateCamera registers a new RTSP camera and broadcasts a config change event.
func (h *CamerasHandler) CreateCamera(c *gin.Context) {
	var req createCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera payload", err))
		return
	}
	id := uuid.Must(uuid.NewV7())
	streamPath := strings.TrimSpace(req.StreamPath)
	if streamPath == "" {
		streamPath = "cam_" + strings.ReplaceAll(id.String(), "-", "")
	}
	codec := strings.TrimSpace(req.CodecHint)
	if codec == "" {
		codec = "h264"
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	cam, err := h.store.Queries().CreateCamera(c.Request.Context(), sqlcgen.CreateCameraParams{
		ID:           id,
		Name:         req.Name,
		Location:     req.Location,
		RtspUrl:      req.RtspURL,
		SubStreamUrl: req.SubStreamURL,
		StreamPath:   streamPath,
		CodecHint:    codec,
		Status:       "offline",
		Enabled:      enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "camera", "created", cam.ID.String())
	respondCreated(c, cam)
}

// UpdateCamera modifies camera metadata and broadcasts a config change event.
func (h *CamerasHandler) UpdateCamera(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	var req updateCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera update payload", err))
		return
	}
	codec := strings.TrimSpace(req.CodecHint)
	if codec == "" {
		codec = "h264"
	}

	cam, err := h.store.Queries().UpdateCamera(c.Request.Context(), sqlcgen.UpdateCameraParams{
		ID:           id,
		Name:         req.Name,
		Location:     req.Location,
		RtspUrl:      req.RtspURL,
		SubStreamUrl: req.SubStreamURL,
		CodecHint:    codec,
		Enabled:      req.Enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "camera", "updated", cam.ID.String())
	respondOK(c, cam)
}

// DeleteCamera removes a camera and broadcasts a config change event.
func (h *CamerasHandler) DeleteCamera(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	if err := h.store.Queries().DeleteCamera(c.Request.Context(), id); err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "camera", "deleted", id.String())
	respondOK(c, gin.H{"deleted": true})
}

// CreateStreamToken issues a 60s Redis-backed WebRTC WHEP playback token for the camera.
func (h *CamerasHandler) CreateStreamToken(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	cam, err := h.store.Queries().GetCameraByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	streamPath := cam.StreamPath
	if c.Query("stream") == "sub" && cam.SubStreamUrl != "" {
		streamPath = cam.StreamPath + "_sub"
	}

	tok, err := auth.GenerateOpaqueToken()
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to generate stream token", err))
		return
	}

	userID := ""
	if claims, ok := auth.CurrentClaims(c); ok {
		userID = claims.UserID
	}
	if h.cache != nil {
		if err := h.cache.SetStreamToken(c.Request.Context(), platformredis.StreamTokenKindPlay, tok, platformredis.StreamTokenPayload{
			UserID:     userID,
			StreamPath: streamPath,
		}, streamPlayTokenTTL); err != nil {
			respondErr(c, domain.NewInternalError("failed to persist stream token", err))
			return
		}
	}

	whepURL := "/whep/" + streamPath + "/whep?token=" + tok
	if h.mediamtx != nil {
		whepURL = h.mediamtx.BuildWHEPPath(streamPath, tok)
	}

	respondOK(c, gin.H{
		"token":       tok,
		"stream_path": streamPath,
		"whep_url":    whepURL,
		"expires_in":  int(streamPlayTokenTTL.Seconds()),
	})
}

// ListROIs returns all ROI regions configured on a camera.
func (h *CamerasHandler) ListROIs(c *gin.Context) {
	camID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	rois, err := h.store.Queries().ListCameraROIsByCamera(c.Request.Context(), camID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, rois)
}

// CreateROI creates a normalized ROI on a camera and broadcasts a config change event.
func (h *CamerasHandler) CreateROI(c *gin.Context) {
	camID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	var req createROIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid roi payload", err))
		return
	}
	if err := ValidateROIPoints(req.ROIType, req.Points); err != nil {
		respondErr(c, err)
		return
	}
	pts := req.Points
	if pts == nil {
		pts = []PointInput{}
	}
	ptsJSON, _ := json.Marshal(pts)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	roi, err := h.store.Queries().CreateCameraROI(c.Request.Context(), sqlcgen.CreateCameraROIParams{
		ID:       uuid.Must(uuid.NewV7()),
		CameraID: camID,
		Name:     req.Name,
		RoiType:  req.ROIType,
		Points:   ptsJSON,
		Enabled:  enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "roi", "created", roi.ID.String())
	respondCreated(c, roi)
}

// UpdateROI updates an existing ROI's vertices or status.
func (h *CamerasHandler) UpdateROI(c *gin.Context) {
	roiID, err := uuid.Parse(c.Param("roi_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid roi id", err))
		return
	}
	var req updateROIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid roi update payload", err))
		return
	}
	if err := ValidateROIPoints(req.ROIType, req.Points); err != nil {
		respondErr(c, err)
		return
	}
	pts := req.Points
	if pts == nil {
		pts = []PointInput{}
	}
	ptsJSON, _ := json.Marshal(pts)

	roi, err := h.store.Queries().UpdateCameraROI(c.Request.Context(), sqlcgen.UpdateCameraROIParams{
		ID:      roiID,
		Name:    req.Name,
		RoiType: req.ROIType,
		Points:  ptsJSON,
		Enabled: req.Enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "roi", "updated", roi.ID.String())
	respondOK(c, roi)
}

// DeleteROI removes a camera ROI.
func (h *CamerasHandler) DeleteROI(c *gin.Context) {
	roiID, err := uuid.Parse(c.Param("roi_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid roi id", err))
		return
	}
	if err := h.store.Queries().DeleteCameraROI(c.Request.Context(), roiID); err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "roi", "deleted", roiID.String())
	respondOK(c, gin.H{"deleted": true})
}
