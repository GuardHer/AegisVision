package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/platform/storage"
)

const (
	defaultDatasetExportRows = 500
	hardMaxDatasetExportRows = 2000
)

// DatasetsHandler serves false-positive sample browsing and training ZIP dataset export.
type DatasetsHandler struct {
	store   *postgres.Store
	storage storage.ObjectStorage
	log     *slog.Logger
}

// NewDatasetsHandler creates a DatasetsHandler.
func NewDatasetsHandler(store *postgres.Store, objStore storage.ObjectStorage, log *slog.Logger) *DatasetsHandler {
	return &DatasetsHandler{store: store, storage: objStore, log: log}
}

// FalsePositiveSampleItem is a single false-positive training sample returned by the list API.
type FalsePositiveSampleItem struct {
	AlarmID             uuid.UUID       `json:"alarm_id"`
	CameraID            uuid.UUID       `json:"camera_id"`
	CameraName          string          `json:"camera_name"`
	ZoneID              *uuid.UUID      `json:"zone_id"`
	ZoneName            string          `json:"zone_name"`
	AlgorithmVersionID  *uuid.UUID      `json:"algorithm_version_id"`
	AlgorithmName       string          `json:"algorithm_name"`
	AlgorithmVersion    string          `json:"algorithm_version"`
	RuleMode            string          `json:"rule_mode"`
	Title               string          `json:"title"`
	Severity            string          `json:"severity"`
	TriggeredAt         time.Time       `json:"triggered_at"`
	HandledAt           *time.Time      `json:"handled_at"`
	FalsePositiveReason string          `json:"false_positive_reason"`
	HandleNote          string          `json:"handle_note"`
	DetectionsSnapshot  json.RawMessage `json:"detections_snapshot"`
	ROISnapshot         json.RawMessage `json:"roi_snapshot"`
	ImageURL            string          `json:"image_url"`
}

// ListFalsePositives returns paginated snapshot-bearing false-positive alarms for algorithm iteration.
func (h *DatasetsHandler) ListFalsePositives(c *gin.Context) {
	ctx := c.Request.Context()
	scope, scopeErr := resolveRequestScope(c, h.store)
	if scopeErr != nil {
		respondErr(c, scopeErr)
		return
	}
	page, pageSize := parsePagination(c, 1, 24, 100)

	var zoneIDs []uuid.UUID
	if rawZone := strings.TrimSpace(c.Query("zone_id")); rawZone != "" {
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
	effectiveZones, emptyScope := scope.IntersectZoneFilter(zoneIDs)
	if emptyScope {
		respondOK(c, gin.H{
			"items":     []FalsePositiveSampleItem{},
			"total":     int64(0),
			"page":      page,
			"page_size": pageSize,
		})
		return
	}
	zoneIDs = effectiveZones

	var algoVerID uuid.NullUUID
	if rawVer := strings.TrimSpace(c.Query("algorithm_version_id")); rawVer != "" {
		vid, err := uuid.Parse(rawVer)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid algorithm_version_id", err))
			return
		}
		algoVerID = uuid.NullUUID{UUID: vid, Valid: true}
	}

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

	reason := toNullableText(c.Query("false_positive_reason"))

	rows, err := h.store.Queries().ListFalsePositiveAlarms(ctx, sqlcgen.ListFalsePositiveAlarmsParams{
		ZoneIds:             zoneIDs,
		AlgorithmVersionID:  algoVerID,
		FalsePositiveReason: reason,
		StartTime:           startTime,
		EndTime:             endTime,
		PageLimit:           int32(pageSize),              // #nosec G115 -- bounded pagination
		PageOffset:          int32((page - 1) * pageSize), // #nosec G115 -- bounded pagination
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	total, err := h.store.Queries().CountFalsePositiveAlarms(ctx, sqlcgen.CountFalsePositiveAlarmsParams{
		ZoneIds:             zoneIDs,
		AlgorithmVersionID:  algoVerID,
		FalsePositiveReason: reason,
		StartTime:           startTime,
		EndTime:             endTime,
	})
	if err != nil {
		respondErr(c, err)
		return
	}

	items := make([]FalsePositiveSampleItem, 0, len(rows))
	for _, r := range rows {
		var zid *uuid.UUID
		if r.ZoneID.Valid {
			v := r.ZoneID.UUID
			zid = &v
		}
		var avid *uuid.UUID
		if r.AlgorithmVersionID.Valid {
			v := r.AlgorithmVersionID.UUID
			avid = &v
		}
		dets := r.DetectionsSnapshot
		if len(dets) == 0 {
			dets = json.RawMessage(`[]`)
		}
		roi := r.RoiSnapshot
		if len(roi) == 0 {
			roi = json.RawMessage(`{}`)
		}
		items = append(items, FalsePositiveSampleItem{
			AlarmID:             r.ID,
			CameraID:            r.CameraID,
			CameraName:          r.CameraName,
			ZoneID:              zid,
			ZoneName:            r.ZoneName,
			AlgorithmVersionID:  avid,
			AlgorithmName:       r.AlgorithmName,
			AlgorithmVersion:    r.AlgorithmVersion,
			RuleMode:            r.RuleMode,
			Title:               r.Title,
			Severity:            r.Severity,
			TriggeredAt:         r.TriggeredAt,
			HandledAt:           r.HandledAt,
			FalsePositiveReason: r.FalsePositiveReason,
			HandleNote:          r.HandleNote,
			DetectionsSnapshot:  dets,
			ROISnapshot:         roi,
			ImageURL:            fmt.Sprintf("/api/v1/evidence/%s/image", r.ID.String()),
		})
	}

	respondOK(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

type exportDatasetRequest struct {
	ZoneID              string `json:"zone_id"`
	AlgorithmVersionID  string `json:"algorithm_version_id"`
	FalsePositiveReason string `json:"false_positive_reason"`
	StartTime           string `json:"start_time"`
	EndTime             string `json:"end_time"`
	Limit               int32  `json:"limit"`
}

func (h *DatasetsHandler) resolveMaxExportRows(ctx context.Context) int32 {
	limit := int32(defaultDatasetExportRows)
	row, err := h.store.Queries().GetSystemSettingByKey(ctx, "platform")
	if err == nil && len(row.Value) > 0 {
		var cfg struct {
			MaxExportRows int32 `json:"max_export_rows"`
		}
		if json.Unmarshal(row.Value, &cfg) == nil && cfg.MaxExportRows > 0 {
			limit = cfg.MaxExportRows
		}
	}
	if limit > hardMaxDatasetExportRows {
		limit = hardMaxDatasetExportRows
	}
	return limit
}

// ExportFalsePositives streams a training ZIP containing images/<alarm_id>.jpg + annotations.json.
func (h *DatasetsHandler) ExportFalsePositives(c *gin.Context) {
	var req exportDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		respondErr(c, domain.NewInvalidArgumentError("invalid dataset export payload", err))
		return
	}

	ctx := c.Request.Context()
	scope, scopeErr := resolveRequestScope(c, h.store)
	if scopeErr != nil {
		respondErr(c, scopeErr)
		return
	}
	maxRows := h.resolveMaxExportRows(ctx)
	limit := req.Limit
	if limit <= 0 || limit > maxRows {
		limit = maxRows
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
	effectiveZones, emptyScope := scope.IntersectZoneFilter(zoneIDs)
	zoneIDs = effectiveZones

	var algoVerID uuid.NullUUID
	if rawVer := strings.TrimSpace(req.AlgorithmVersionID); rawVer != "" {
		vid, err := uuid.Parse(rawVer)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid algorithm_version_id", err))
			return
		}
		algoVerID = uuid.NullUUID{UUID: vid, Valid: true}
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

	var rows []sqlcgen.ListFalsePositiveAlarmsForExportRow
	if !emptyScope {
		var err error
		rows, err = h.store.Queries().ListFalsePositiveAlarmsForExport(ctx, sqlcgen.ListFalsePositiveAlarmsForExportParams{
			ZoneIds:             zoneIDs,
			AlgorithmVersionID:  algoVerID,
			FalsePositiveReason: toNullableText(req.FalsePositiveReason),
			StartTime:           startTime,
			EndTime:             endTime,
			MaxRows:             limit,
		})
		if err != nil {
			respondErr(c, err)
			return
		}
	}

	tsTag := time.Now().UTC().Format("20060102-150405")
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="aegis-fp-dataset-%s.zip"`, tsTag))
	c.Status(http.StatusOK)

	zw := zip.NewWriter(c.Writer)
	defer func() {
		if closeErr := zw.Close(); closeErr != nil && h.log != nil {
			h.log.Warn("dataset zip close failed", "err", closeErr)
		}
	}()

	type annotationEntry struct {
		AlarmID             string          `json:"alarm_id"`
		ImageFile           string          `json:"image_file"`
		CameraName          string          `json:"camera_name"`
		ZoneName            string          `json:"zone_name"`
		AlgorithmName       string          `json:"algorithm_name"`
		AlgorithmVersion    string          `json:"algorithm_version"`
		RuleMode            string          `json:"rule_mode"`
		TriggeredAt         string          `json:"triggered_at"`
		Detections          json.RawMessage `json:"detections"`
		ROI                 json.RawMessage `json:"roi"`
		FalsePositiveReason string          `json:"false_positive_reason"`
		HandleNote          string          `json:"handle_note"`
	}

	annotations := make([]annotationEntry, 0, len(rows))
	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		imgFile := ""
		if r.ImageObjectKey != "" && h.storage != nil {
			rc, _, getErr := h.storage.GetObject(ctx, r.ImageObjectKey, "")
			if getErr != nil {
				if h.log != nil {
					h.log.Warn("dataset export fetch snapshot failed", "alarm_id", r.ID, "err", getErr)
				}
			} else {
				name := fmt.Sprintf("images/%s.jpg", r.ID.String())
				if fw, zErr := zw.Create(name); zErr == nil {
					if _, copyErr := io.Copy(fw, rc); copyErr == nil {
						imgFile = name
					}
				}
				_ = rc.Close()
			}
		}
		dets := r.DetectionsSnapshot
		if len(dets) == 0 {
			dets = json.RawMessage(`[]`)
		}
		roi := r.RoiSnapshot
		if len(roi) == 0 {
			roi = json.RawMessage(`{}`)
		}
		annotations = append(annotations, annotationEntry{
			AlarmID:             r.ID.String(),
			ImageFile:           imgFile,
			CameraName:          r.CameraName,
			ZoneName:            r.ZoneName,
			AlgorithmName:       r.AlgorithmName,
			AlgorithmVersion:    r.AlgorithmVersion,
			RuleMode:            r.RuleMode,
			TriggeredAt:         r.TriggeredAt.UTC().Format(time.RFC3339),
			Detections:          dets,
			ROI:                 roi,
			FalsePositiveReason: r.FalsePositiveReason,
			HandleNote:          r.HandleNote,
		})
	}

	if ctx.Err() == nil {
		if fw, zErr := zw.Create("annotations.json"); zErr == nil {
			enc := json.NewEncoder(fw)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(annotations); encErr != nil && h.log != nil {
				h.log.Warn("dataset export encode annotations failed", "err", encErr)
			}
		}
	}
}
