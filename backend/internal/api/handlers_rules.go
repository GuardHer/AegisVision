package api

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// RulesHandler manages ROI × AlgorithmVersion rules and linked alarm channels.
type RulesHandler struct {
	store *postgres.Store
	pub   EventPublisher
	log   *slog.Logger
}

// NewRulesHandler constructs a RulesHandler.
func NewRulesHandler(store *postgres.Store, pub EventPublisher, log *slog.Logger) *RulesHandler {
	return &RulesHandler{store: store, pub: pub, log: log}
}

type createRuleRequest struct {
	ROIID              string          `json:"roi_id" binding:"required"`
	AlgorithmVersionID string          `json:"algorithm_version_id" binding:"required"`
	Name               string          `json:"name" binding:"required"`
	RuleMode           string          `json:"rule_mode" binding:"required"`
	Params             json.RawMessage `json:"params" binding:"required"`
	AlgoParamsOverride json.RawMessage `json:"algo_params_override"`
	ScheduleMode       string          `json:"schedule_mode"`
	PinnedNodeID       string          `json:"pinned_node_id"`
	RecordVideo        bool            `json:"record_video"`
	VideoPreSeconds    *int32          `json:"video_pre_seconds"`
	VideoPostSeconds   *int32          `json:"video_post_seconds"`
	Enabled            *bool           `json:"enabled"`
	ChannelIDs         []string        `json:"channel_ids"`
}

type updateRuleRequest struct {
	Name               string          `json:"name" binding:"required"`
	RuleMode           string          `json:"rule_mode" binding:"required"`
	Params             json.RawMessage `json:"params" binding:"required"`
	AlgoParamsOverride json.RawMessage `json:"algo_params_override"`
	ScheduleMode       string          `json:"schedule_mode"`
	PinnedNodeID       string          `json:"pinned_node_id"`
	RecordVideo        bool            `json:"record_video"`
	VideoPreSeconds    *int32          `json:"video_pre_seconds"`
	VideoPostSeconds   *int32          `json:"video_post_seconds"`
	Enabled            bool            `json:"enabled"`
	ChannelIDs         []string        `json:"channel_ids"`
}

func isValidRuleMode(mode string) bool {
	return mode == domain.RuleModeAnomalyDetect ||
		mode == domain.RuleModeCountThreshold ||
		mode == domain.RuleModeAbsenceDetect
}

func toNullableText(s string) pgtype.Text {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: trimmed, Valid: true}
}

func parseUUIDSlice(raw []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// ListByCamera returns all rules configured under a camera.
func (h *RulesHandler) ListByCamera(c *gin.Context) {
	camID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	rules, err := h.store.Queries().ListRulesByCamera(c.Request.Context(), camID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, rules)
}

// Create creates a new ROI × algorithm_version rule and links any alarm channels atomically.
func (h *RulesHandler) Create(c *gin.Context) {
	camID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid camera id", err))
		return
	}
	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule payload", err))
		return
	}
	if !isValidRuleMode(req.RuleMode) {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule_mode", nil))
		return
	}
	roiID, err := uuid.Parse(req.ROIID)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid roi_id", err))
		return
	}
	verID, err := uuid.Parse(req.AlgorithmVersionID)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid algorithm_version_id", err))
		return
	}
	channelIDs, err := parseUUIDSlice(req.ChannelIDs)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel_ids", err))
		return
	}

	schedMode := strings.TrimSpace(req.ScheduleMode)
	if schedMode == "" {
		schedMode = "auto"
	}
	if schedMode != "auto" && schedMode != "pinned" {
		respondErr(c, domain.NewInvalidArgumentError("schedule_mode must be auto or pinned", nil))
		return
	}
	if schedMode == "pinned" && strings.TrimSpace(req.PinnedNodeID) == "" {
		respondErr(c, domain.NewInvalidArgumentError("pinned_node_id is required when schedule_mode=pinned", nil))
		return
	}

	preSec := int32(5)
	if req.VideoPreSeconds != nil && *req.VideoPreSeconds >= 0 {
		preSec = *req.VideoPreSeconds
	}
	postSec := int32(5)
	if req.VideoPostSeconds != nil && *req.VideoPostSeconds >= 0 {
		postSec = *req.VideoPostSeconds
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	ctx := c.Request.Context()
	var created sqlcgen.RoiAlgorithmRule
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var txErr error
		created, txErr = q.CreateROIAlgorithmRule(ctx, sqlcgen.CreateROIAlgorithmRuleParams{
			ID:                 uuid.Must(uuid.NewV7()),
			CameraID:           camID,
			RoiID:              roiID,
			AlgorithmVersionID: verID,
			Name:               req.Name,
			RuleMode:           req.RuleMode,
			Params:             req.Params,
			AlgoParamsOverride: defaultJSONObject(req.AlgoParamsOverride),
			ScheduleMode:       schedMode,
			PinnedNodeID:       toNullableText(req.PinnedNodeID),
			RecordVideo:        req.RecordVideo,
			VideoPreSeconds:    preSec,
			VideoPostSeconds:   postSec,
			Enabled:            enabled,
		})
		if txErr != nil {
			return txErr
		}
		for _, chID := range channelIDs {
			if txErr = q.LinkRuleAlarmChannel(ctx, sqlcgen.LinkRuleAlarmChannelParams{
				RuleID:    created.ID,
				ChannelID: chID,
			}); txErr != nil {
				return txErr
			}
		}
		return nil
	}); err != nil {
		respondErr(c, err)
		return
	}

	emitConfigEvent(ctx, h.pub, h.log, "rule", "created", created.ID.String())
	respondCreated(c, created)
}

// Update modifies an existing rule and replaces its channel bindings if channel_ids is supplied.
func (h *RulesHandler) Update(c *gin.Context) {
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule id", err))
		return
	}
	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule update payload", err))
		return
	}
	if !isValidRuleMode(req.RuleMode) {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule_mode", nil))
		return
	}
	schedMode := strings.TrimSpace(req.ScheduleMode)
	if schedMode == "" {
		schedMode = "auto"
	}
	if schedMode != "auto" && schedMode != "pinned" {
		respondErr(c, domain.NewInvalidArgumentError("schedule_mode must be auto or pinned", nil))
		return
	}
	if schedMode == "pinned" && strings.TrimSpace(req.PinnedNodeID) == "" {
		respondErr(c, domain.NewInvalidArgumentError("pinned_node_id is required when schedule_mode=pinned", nil))
		return
	}
	channelIDs, err := parseUUIDSlice(req.ChannelIDs)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid channel_ids", err))
		return
	}

	preSec := int32(5)
	if req.VideoPreSeconds != nil && *req.VideoPreSeconds >= 0 {
		preSec = *req.VideoPreSeconds
	}
	postSec := int32(5)
	if req.VideoPostSeconds != nil && *req.VideoPostSeconds >= 0 {
		postSec = *req.VideoPostSeconds
	}

	ctx := c.Request.Context()
	var updated sqlcgen.RoiAlgorithmRule
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var txErr error
		updated, txErr = q.UpdateROIAlgorithmRule(ctx, sqlcgen.UpdateROIAlgorithmRuleParams{
			ID:                 ruleID,
			Name:               req.Name,
			RuleMode:           req.RuleMode,
			Params:             req.Params,
			AlgoParamsOverride: defaultJSONObject(req.AlgoParamsOverride),
			ScheduleMode:       schedMode,
			PinnedNodeID:       toNullableText(req.PinnedNodeID),
			RecordVideo:        req.RecordVideo,
			VideoPreSeconds:    preSec,
			VideoPostSeconds:   postSec,
			Enabled:            req.Enabled,
		})
		if txErr != nil {
			return txErr
		}
		if req.ChannelIDs != nil {
			if txErr = q.ClearRuleAlarmChannels(ctx, ruleID); txErr != nil {
				return txErr
			}
			for _, chID := range channelIDs {
				if txErr = q.LinkRuleAlarmChannel(ctx, sqlcgen.LinkRuleAlarmChannelParams{
					RuleID:    ruleID,
					ChannelID: chID,
				}); txErr != nil {
					return txErr
				}
			}
		}
		return nil
	}); err != nil {
		respondErr(c, err)
		return
	}

	emitConfigEvent(ctx, h.pub, h.log, "rule", "updated", updated.ID.String())
	respondOK(c, updated)
}

// Delete removes a rule and notifies scheduler/ingest to reconcile.
func (h *RulesHandler) Delete(c *gin.Context) {
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid rule id", err))
		return
	}
	if err := h.store.Queries().DeleteROIAlgorithmRule(c.Request.Context(), ruleID); err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "rule", "deleted", ruleID.String())
	respondOK(c, gin.H{"deleted": true})
}
