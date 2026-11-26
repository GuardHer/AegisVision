package api

import (
	"encoding/json"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/GuardHer/AegisVision/backend/internal/domain"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres"
	"github.com/GuardHer/AegisVision/backend/internal/platform/postgres/sqlcgen"
)

// AlgorithmsHandler manages algorithms and semantic algorithm versions.
type AlgorithmsHandler struct {
	store *postgres.Store
	pub   EventPublisher
	log   *slog.Logger
}

// NewAlgorithmsHandler constructs an AlgorithmsHandler.
func NewAlgorithmsHandler(store *postgres.Store, pub EventPublisher, log *slog.Logger) *AlgorithmsHandler {
	return &AlgorithmsHandler{store: store, pub: pub, log: log}
}

type createAlgorithmRequest struct {
	AlgorithmKey    string   `json:"algorithm_key" binding:"required"`
	Name            string   `json:"name" binding:"required"`
	Description     string   `json:"description"`
	SupportedLabels []string `json:"supported_labels"`
}

type updateAlgorithmRequest struct {
	Name            string   `json:"name" binding:"required"`
	Description     string   `json:"description"`
	SupportedLabels []string `json:"supported_labels"`
}

type createVersionRequest struct {
	Version       string          `json:"version" binding:"required"`
	RuntimeKey    string          `json:"runtime_key" binding:"required"`
	LaunchSpec    json.RawMessage `json:"launch_spec"`
	DefaultParams json.RawMessage `json:"default_params"`
	Status        string          `json:"status"`
}

type updateVersionRequest struct {
	RuntimeKey    string          `json:"runtime_key" binding:"required"`
	LaunchSpec    json.RawMessage `json:"launch_spec"`
	DefaultParams json.RawMessage `json:"default_params"`
	Status        string          `json:"status" binding:"required"`
}

func defaultJSONObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// ListAlgorithms returns all registered algorithms.
func (h *AlgorithmsHandler) ListAlgorithms(c *gin.Context) {
	items, err := h.store.Queries().ListAlgorithms(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// CreateAlgorithm inserts a new algorithm catalog entry.
func (h *AlgorithmsHandler) CreateAlgorithm(c *gin.Context) {
	var req createAlgorithmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm payload", err))
		return
	}
	if req.SupportedLabels == nil {
		req.SupportedLabels = []string{}
	}
	labelsJSON, err := json.Marshal(req.SupportedLabels)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid supported_labels", err))
		return
	}

	item, err := h.store.Queries().CreateAlgorithm(c.Request.Context(), sqlcgen.CreateAlgorithmParams{
		ID:              uuid.Must(uuid.NewV7()),
		AlgorithmKey:    req.AlgorithmKey,
		Name:            req.Name,
		Description:     req.Description,
		SupportedLabels: labelsJSON,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondCreated(c, item)
}

// UpdateAlgorithm updates an existing algorithm entry.
func (h *AlgorithmsHandler) UpdateAlgorithm(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm id", err))
		return
	}
	var req updateAlgorithmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm payload", err))
		return
	}
	if req.SupportedLabels == nil {
		req.SupportedLabels = []string{}
	}
	labelsJSON, _ := json.Marshal(req.SupportedLabels)

	item, err := h.store.Queries().UpdateAlgorithm(c.Request.Context(), sqlcgen.UpdateAlgorithmParams{
		ID:              id,
		Name:            req.Name,
		Description:     req.Description,
		SupportedLabels: labelsJSON,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, item)
}

// DeleteAlgorithm deletes an algorithm entry (restricted if versions exist).
func (h *AlgorithmsHandler) DeleteAlgorithm(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm id", err))
		return
	}
	if err := h.store.Queries().DeleteAlgorithm(c.Request.Context(), id); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}

// ListVersions returns all versions under an algorithm.
func (h *AlgorithmsHandler) ListVersions(c *gin.Context) {
	algoID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm id", err))
		return
	}
	items, err := h.store.Queries().ListAlgorithmVersionsByAlgorithm(c.Request.Context(), algoID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// CreateVersion registers a new version bound to a runtime_key.
func (h *AlgorithmsHandler) CreateVersion(c *gin.Context) {
	algoID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm id", err))
		return
	}
	var req createVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid version payload", err))
		return
	}
	status := req.Status
	if status == "" {
		status = "active"
	}

	item, err := h.store.Queries().CreateAlgorithmVersion(c.Request.Context(), sqlcgen.CreateAlgorithmVersionParams{
		ID:            uuid.Must(uuid.NewV7()),
		AlgorithmID:   algoID,
		Version:       req.Version,
		RuntimeKey:    req.RuntimeKey,
		LaunchSpec:    defaultJSONObject(req.LaunchSpec),
		DefaultParams: defaultJSONObject(req.DefaultParams),
		Status:        status,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "algorithm_version", "created", item.ID.String())
	respondCreated(c, item)
}

// UpdateVersion modifies an algorithm version's runtime_key, params, or lifecycle status.
func (h *AlgorithmsHandler) UpdateVersion(c *gin.Context) {
	verID, err := uuid.Parse(c.Param("version_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid version id", err))
		return
	}
	var req updateVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid version update payload", err))
		return
	}

	item, err := h.store.Queries().UpdateAlgorithmVersion(c.Request.Context(), sqlcgen.UpdateAlgorithmVersionParams{
		ID:            verID,
		RuntimeKey:    req.RuntimeKey,
		LaunchSpec:    defaultJSONObject(req.LaunchSpec),
		DefaultParams: defaultJSONObject(req.DefaultParams),
		Status:        req.Status,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "algorithm_version", "updated", item.ID.String())
	respondOK(c, item)
}
