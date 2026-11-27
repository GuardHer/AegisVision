package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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
	Name            string   `json:"name" binding:"required"`
	Location        string   `json:"location"`
	RtspURL         string   `json:"rtsp_url" binding:"required"`
	SubStreamURL    string   `json:"sub_stream_url"`
	StreamPath      string   `json:"stream_path"`
	CodecHint       string   `json:"codec_hint"`
	Enabled         *bool    `json:"enabled"`
	ZoneID          string   `json:"zone_id"`
	WorkstationCode string   `json:"workstation_code"`
	Tags            []string `json:"tags"`
}

type updateCameraRequest struct {
	Name            string   `json:"name" binding:"required"`
	Location        string   `json:"location"`
	RtspURL         string   `json:"rtsp_url" binding:"required"`
	SubStreamURL    string   `json:"sub_stream_url"`
	CodecHint       string   `json:"codec_hint"`
	Enabled         bool     `json:"enabled"`
	ZoneID          string   `json:"zone_id"`
	WorkstationCode string   `json:"workstation_code"`
	Tags            []string `json:"tags"`
}

type batchCamerasRequest struct {
	CameraIDs []string `json:"camera_ids" binding:"required"`
	Action    string   `json:"action" binding:"required"`
	ZoneID    string   `json:"zone_id"`
}

type importCamerasCSVRequest struct {
	CSV string `json:"csv"`
}

type createROIRequest struct {
	Name    string       `json:"name" binding:"required"`
	ROIType string       `json:"roi_type" binding:"required"`
	Points  []PointInput `json:"points"`
	Enabled *bool        `json:"enabled"`
	Color   string       `json:"color"`
}

type updateROIRequest struct {
	Name    string       `json:"name" binding:"required"`
	ROIType string       `json:"roi_type" binding:"required"`
	Points  []PointInput `json:"points"`
	Enabled bool         `json:"enabled"`
	Color   string       `json:"color"`
}

// ListCameras returns configured cameras, optionally filtered by zone_id (including descendant zones).
func (h *CamerasHandler) ListCameras(c *gin.Context) {
	ctx := c.Request.Context()
	items, err := h.store.Queries().ListCameras(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	if rawZone := strings.TrimSpace(c.Query("zone_id")); rawZone != "" {
		zid, parseErr := uuid.Parse(rawZone)
		if parseErr != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid zone_id filter", parseErr))
			return
		}
		allowed, descErr := collectZoneDescendants(ctx, h.store.Queries(), zid)
		if descErr != nil {
			respondErr(c, descErr)
			return
		}
		allowedSet := make(map[uuid.UUID]struct{}, len(allowed))
		for _, id := range allowed {
			allowedSet[id] = struct{}{}
		}
		filtered := make([]sqlcgen.Camera, 0, len(items))
		for _, cam := range items {
			if cam.ZoneID.Valid {
				if _, ok := allowedSet[cam.ZoneID.UUID]; ok {
					filtered = append(filtered, cam)
				}
			}
		}
		items = filtered
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

	var zoneID uuid.NullUUID
	if trimmed := strings.TrimSpace(req.ZoneID); trimmed != "" {
		zid, parseErr := uuid.Parse(trimmed)
		if parseErr != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid zone_id", parseErr))
			return
		}
		zoneID = uuid.NullUUID{UUID: zid, Valid: true}
	}
	tags := req.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, _ := json.Marshal(tags)

	cam, err := h.store.Queries().CreateCamera(c.Request.Context(), sqlcgen.CreateCameraParams{
		ID:              id,
		Name:            req.Name,
		Location:        req.Location,
		RtspUrl:         req.RtspURL,
		SubStreamUrl:    req.SubStreamURL,
		StreamPath:      streamPath,
		CodecHint:       codec,
		Status:          "offline",
		Enabled:         enabled,
		ZoneID:          zoneID,
		WorkstationCode: strings.TrimSpace(req.WorkstationCode),
		Tags:            tagsJSON,
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

	var zoneID uuid.NullUUID
	if trimmed := strings.TrimSpace(req.ZoneID); trimmed != "" {
		zid, parseErr := uuid.Parse(trimmed)
		if parseErr != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid zone_id", parseErr))
			return
		}
		zoneID = uuid.NullUUID{UUID: zid, Valid: true}
	}
	tags := req.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, _ := json.Marshal(tags)

	cam, err := h.store.Queries().UpdateCamera(c.Request.Context(), sqlcgen.UpdateCameraParams{
		ID:              id,
		Name:            req.Name,
		Location:        req.Location,
		RtspUrl:         req.RtspURL,
		SubStreamUrl:    req.SubStreamURL,
		CodecHint:       codec,
		Enabled:         req.Enabled,
		ZoneID:          zoneID,
		WorkstationCode: strings.TrimSpace(req.WorkstationCode),
		Tags:            tagsJSON,
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

// BatchUpdate applies enable, disable, or move_zone across multiple cameras atomically.
func (h *CamerasHandler) BatchUpdate(c *gin.Context) {
	var req batchCamerasRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.CameraIDs) == 0 {
		respondErr(c, domain.NewInvalidArgumentError("camera_ids and action are required", err))
		return
	}
	ids, err := parseUUIDSlice(req.CameraIDs)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera_ids", err))
		return
	}

	ctx := c.Request.Context()
	var affected int64
	switch req.Action {
	case "enable":
		affected, err = h.store.Queries().BatchUpdateCamerasEnabled(ctx, sqlcgen.BatchUpdateCamerasEnabledParams{
			Enabled: true,
			Ids:     ids,
		})
	case "disable":
		affected, err = h.store.Queries().BatchUpdateCamerasEnabled(ctx, sqlcgen.BatchUpdateCamerasEnabledParams{
			Enabled: false,
			Ids:     ids,
		})
	case "move_zone":
		var zoneID uuid.NullUUID
		if trimmed := strings.TrimSpace(req.ZoneID); trimmed != "" {
			zid, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				respondErr(c, domain.NewInvalidArgumentError("invalid zone_id", parseErr))
				return
			}
			zoneID = uuid.NullUUID{UUID: zid, Valid: true}
		}
		affected, err = h.store.Queries().BatchUpdateCamerasZone(ctx, sqlcgen.BatchUpdateCamerasZoneParams{
			ZoneID: zoneID,
			Ids:    ids,
		})
	default:
		respondErr(c, domain.NewInvalidArgumentError("action must be enable, disable, or move_zone", nil))
		return
	}
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(ctx, h.pub, h.log, "camera", "batch_"+req.Action, "")
	respondOK(c, gin.H{"updated_count": affected})
}

// ImportCSV batch-creates cameras from CSV text (columns: name,rtsp_url,sub_stream_url,workstation_code,zone_id,codec_hint).
func (h *CamerasHandler) ImportCSV(c *gin.Context) {
	var rawCSV string
	if strings.Contains(c.ContentType(), "application/json") {
		var req importCamerasCSVRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid import payload", err))
			return
		}
		rawCSV = req.CSV
	} else {
		bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 2*1024*1024))
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("failed to read csv body", err))
			return
		}
		rawCSV = string(bodyBytes)
	}
	rawCSV = strings.TrimPrefix(strings.TrimSpace(rawCSV), "\uFEFF")
	if rawCSV == "" {
		respondErr(c, domain.NewInvalidArgumentError("csv content is empty", nil))
		return
	}

	reader := csv.NewReader(strings.NewReader(rawCSV))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		respondErr(c, domain.NewInvalidArgumentError("failed to parse csv rows", err))
		return
	}

	startIdx := 0
	if len(rows[0]) >= 2 && strings.EqualFold(strings.TrimSpace(rows[0][0]), "name") {
		startIdx = 1
	}

	ctx := c.Request.Context()
	created := make([]sqlcgen.Camera, 0)
	rowErrors := make([]string, 0)

	for i := startIdx; i < len(rows); i++ {
		cols := rows[i]
		getCol := func(idx int) string {
			if idx < len(cols) {
				return strings.TrimSpace(cols[idx])
			}
			return ""
		}
		name := getCol(0)
		rtspURL := getCol(1)
		if name == "" || rtspURL == "" {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d: name and rtsp_url are required", i+1))
			continue
		}
		subURL := getCol(2)
		wsCode := getCol(3)
		var zoneID uuid.NullUUID
		if zRaw := getCol(4); zRaw != "" {
			zid, parseErr := uuid.Parse(zRaw)
			if parseErr != nil {
				rowErrors = append(rowErrors, fmt.Sprintf("row %d: invalid zone_id %q", i+1, zRaw))
				continue
			}
			zoneID = uuid.NullUUID{UUID: zid, Valid: true}
		}
		codec := getCol(5)
		if codec == "" {
			codec = "h264"
		}

		id := uuid.Must(uuid.NewV7())
		cam, createErr := h.store.Queries().CreateCamera(ctx, sqlcgen.CreateCameraParams{
			ID:              id,
			Name:            name,
			Location:        "",
			RtspUrl:         rtspURL,
			SubStreamUrl:    subURL,
			StreamPath:      "cam_" + strings.ReplaceAll(id.String(), "-", ""),
			CodecHint:       codec,
			Status:          "offline",
			Enabled:         true,
			ZoneID:          zoneID,
			WorkstationCode: wsCode,
			Tags:            []byte(`[]`),
		})
		if createErr != nil {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d: %v", i+1, createErr))
			continue
		}
		created = append(created, cam)
	}

	if len(created) > 0 {
		emitConfigEvent(ctx, h.pub, h.log, "camera", "imported", "")
	}
	respondOK(c, gin.H{
		"created_count": len(created),
		"cameras":       created,
		"errors":        rowErrors,
	})
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
		Color:    strings.TrimSpace(req.Color),
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
		Color:   strings.TrimSpace(req.Color),
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
