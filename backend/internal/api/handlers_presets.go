package api

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// PresetsHandler manages saved multi-split monitor layouts and timed tour presets.
type PresetsHandler struct {
	store *postgres.Store
}

// NewPresetsHandler constructs a PresetsHandler.
func NewPresetsHandler(store *postgres.Store) *PresetsHandler {
	return &PresetsHandler{store: store}
}

type presetMutationRequest struct {
	Name                string   `json:"name" binding:"required"`
	IsShared            bool     `json:"is_shared"`
	GridSize            int32    `json:"grid_size" binding:"required"`
	CameraIDs           []string `json:"camera_ids"`
	TourIntervalSeconds int32    `json:"tour_interval_seconds"`
}

func isValidGridSize(n int32) bool {
	return n == 1 || n == 4 || n == 9 || n == 16
}

func currentUserContext(c *gin.Context) (uuid.NullUUID, string, bool) {
	claims, ok := auth.CurrentClaims(c)
	if !ok {
		return uuid.NullUUID{}, "", false
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		return uuid.NullUUID{}, claims.Role, true
	}
	return uuid.NullUUID{UUID: uid, Valid: true}, claims.Role, true
}

// List returns shared presets plus private presets owned by the authenticated user.
func (h *PresetsHandler) List(c *gin.Context) {
	ownerID, _, ok := currentUserContext(c)
	if !ok {
		respondErr(c, domain.NewUnauthorizedError("authentication required", nil))
		return
	}
	items, err := h.store.Queries().ListMonitorPresetsForUser(c.Request.Context(), ownerID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// Create saves a new private or (admin-only) shared monitor preset.
func (h *PresetsHandler) Create(c *gin.Context) {
	ownerID, role, ok := currentUserContext(c)
	if !ok {
		respondErr(c, domain.NewUnauthorizedError("authentication required", nil))
		return
	}
	var req presetMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid monitor preset payload", err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondErr(c, domain.NewInvalidArgumentError("preset name is required", nil))
		return
	}
	if !isValidGridSize(req.GridSize) {
		respondErr(c, domain.NewInvalidArgumentError("grid_size must be 1, 4, 9, or 16", nil))
		return
	}
	if req.TourIntervalSeconds < 0 {
		respondErr(c, domain.NewInvalidArgumentError("tour_interval_seconds must be >= 0", nil))
		return
	}
	if req.IsShared && role != domain.RoleAdmin {
		respondErr(c, domain.NewForbiddenError("only admin can create shared monitor presets", nil))
		return
	}
	cams := req.CameraIDs
	if cams == nil {
		cams = []string{}
	}
	camsJSON, _ := json.Marshal(cams)

	created, err := h.store.Queries().CreateMonitorPreset(c.Request.Context(), sqlcgen.CreateMonitorPresetParams{
		ID:                  uuid.Must(uuid.NewV7()),
		OwnerUserID:         ownerID,
		Name:                name,
		IsShared:            req.IsShared,
		GridSize:            req.GridSize,
		CameraIds:           camsJSON,
		TourIntervalSeconds: req.TourIntervalSeconds,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondCreated(c, created)
}

// Update modifies a preset owned by the user (or any preset for admins).
func (h *PresetsHandler) Update(c *gin.Context) {
	ownerID, role, ok := currentUserContext(c)
	if !ok {
		respondErr(c, domain.NewUnauthorizedError("authentication required", nil))
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid preset id", err))
		return
	}
	existing, err := h.store.Queries().GetMonitorPresetByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if role != domain.RoleAdmin && (existing.IsShared || !existing.OwnerUserID.Valid || existing.OwnerUserID.UUID != ownerID.UUID) {
		respondErr(c, domain.NewForbiddenError("insufficient permissions to modify this preset", nil))
		return
	}

	var req presetMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid monitor preset payload", err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondErr(c, domain.NewInvalidArgumentError("preset name is required", nil))
		return
	}
	if !isValidGridSize(req.GridSize) {
		respondErr(c, domain.NewInvalidArgumentError("grid_size must be 1, 4, 9, or 16", nil))
		return
	}
	if req.TourIntervalSeconds < 0 {
		respondErr(c, domain.NewInvalidArgumentError("tour_interval_seconds must be >= 0", nil))
		return
	}
	if req.IsShared && role != domain.RoleAdmin {
		respondErr(c, domain.NewForbiddenError("only admin can publish shared monitor presets", nil))
		return
	}

	cams := req.CameraIDs
	if cams == nil {
		cams = []string{}
	}
	camsJSON, _ := json.Marshal(cams)

	updated, err := h.store.Queries().UpdateMonitorPreset(c.Request.Context(), sqlcgen.UpdateMonitorPresetParams{
		ID:                  id,
		Name:                name,
		IsShared:            req.IsShared,
		GridSize:            req.GridSize,
		CameraIds:           camsJSON,
		TourIntervalSeconds: req.TourIntervalSeconds,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, updated)
}

// Delete removes a preset owned by the user (or any preset for admins).
func (h *PresetsHandler) Delete(c *gin.Context) {
	ownerID, role, ok := currentUserContext(c)
	if !ok {
		respondErr(c, domain.NewUnauthorizedError("authentication required", nil))
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid preset id", err))
		return
	}
	existing, err := h.store.Queries().GetMonitorPresetByID(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if role != domain.RoleAdmin && (existing.IsShared || !existing.OwnerUserID.Valid || existing.OwnerUserID.UUID != ownerID.UUID) {
		respondErr(c, domain.NewForbiddenError("insufficient permissions to delete this preset", nil))
		return
	}
	if err := h.store.Queries().DeleteMonitorPreset(c.Request.Context(), id); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}
