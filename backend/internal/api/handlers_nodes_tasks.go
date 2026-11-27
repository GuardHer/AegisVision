package api

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// NodesTasksHandler exposes compute node monitoring, draining, and inference task inspection.
type NodesTasksHandler struct {
	store *postgres.Store
	pub   EventPublisher
	log   *slog.Logger
}

// NewNodesTasksHandler constructs a NodesTasksHandler.
func NewNodesTasksHandler(store *postgres.Store, pub EventPublisher, log *slog.Logger) *NodesTasksHandler {
	return &NodesTasksHandler{store: store, pub: pub, log: log}
}

type updateNodeStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// ListNodes returns all registered aegis-agent compute nodes.
func (h *NodesTasksHandler) ListNodes(c *gin.Context) {
	nodes, err := h.store.Queries().ListAgentNodes(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, nodes)
}

// GetNode returns details for a specific compute node.
func (h *NodesTasksHandler) GetNode(c *gin.Context) {
	nodeID := c.Param("node_id")
	node, err := h.store.Queries().GetAgentNodeByID(c.Request.Context(), nodeID)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, node)
}

// UpdateNodeStatus transitions a node status (e.g., online / draining / offline) and triggers reconciliation.
func (h *NodesTasksHandler) UpdateNodeStatus(c *gin.Context) {
	nodeID := c.Param("node_id")
	var req updateNodeStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("status is required", err))
		return
	}
	if req.Status != domain.NodeStatusOnline && req.Status != domain.NodeStatusOffline && req.Status != domain.NodeStatusDraining {
		respondErr(c, domain.NewInvalidArgumentError("status must be online, offline, or draining", nil))
		return
	}

	node, err := h.store.Queries().UpdateAgentNodeStatus(c.Request.Context(), sqlcgen.UpdateAgentNodeStatusParams{
		NodeID: nodeID,
		Status: req.Status,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "node", "status_changed", nodeID)
	recordAudit(c, h.store, h.log, "node.update_status", "node", nodeID, "Changed node status to "+req.Status, map[string]any{"status": req.Status})
	respondOK(c, node)
}

// ListTasks returns all inference tasks.
func (h *NodesTasksHandler) ListTasks(c *gin.Context) {
	tasks, err := h.store.Queries().ListInferenceTasks(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, tasks)
}

// RescheduleTask clears the current assignment and signals aegis-scheduler to re-evaluate placement.
func (h *NodesTasksHandler) RescheduleTask(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid task id", err))
		return
	}
	if _, err := h.store.Queries().ResetInferenceTaskForReschedule(c.Request.Context(), taskID); err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "task", "reschedule", taskID.String())
	recordAudit(c, h.store, h.log, "task.reschedule", "node", taskID.String(), "Requested task reschedule", nil)
	respondOK(c, gin.H{"reschedule_requested": true, "task_id": taskID})
}
