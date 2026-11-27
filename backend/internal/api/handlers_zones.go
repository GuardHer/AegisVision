package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// ZonesHandler manages the Factory -> Workshop -> Line -> Workstation spatial hierarchy.
type ZonesHandler struct {
	store *postgres.Store
	cache CacheStore
	pub   EventPublisher
	log   *slog.Logger
}

// NewZonesHandler constructs a ZonesHandler.
func NewZonesHandler(store *postgres.Store, cache CacheStore, pub EventPublisher, log *slog.Logger) *ZonesHandler {
	return &ZonesHandler{store: store, cache: cache, pub: pub, log: log}
}

// ZoneTreeNode is a recursive spatial node with rolled-up camera counts.
type ZoneTreeNode struct {
	ID                uuid.UUID       `json:"id"`
	ParentID          *uuid.UUID      `json:"parent_id"`
	Name              string          `json:"name"`
	Code              string          `json:"code"`
	ZoneType          string          `json:"zone_type"`
	SortOrder         int32           `json:"sort_order"`
	Description       string          `json:"description"`
	CameraCount       int64           `json:"camera_count"`
	OnlineCameraCount int64           `json:"online_camera_count"`
	Children          []*ZoneTreeNode `json:"children"`
}

type zoneMutationRequest struct {
	ParentID    string `json:"parent_id"`
	Name        string `json:"name" binding:"required"`
	Code        string `json:"code"`
	ZoneType    string `json:"zone_type" binding:"required"`
	SortOrder   int32  `json:"sort_order"`
	Description string `json:"description"`
}

func isValidZoneType(zt string) bool {
	switch zt {
	case domain.ZoneTypeFactory, domain.ZoneTypeWorkshop, domain.ZoneTypeLine, domain.ZoneTypeWorkstation:
		return true
	default:
		return false
	}
}

// GetTree returns the full spatial tree with rolled-up camera and online camera counts (scoped by workshop RBAC).
func (h *ZonesHandler) GetTree(c *gin.Context) {
	ctx := c.Request.Context()
	scope, scopeErr := resolveRequestScope(c, h.store)
	if scopeErr != nil {
		respondErr(c, scopeErr)
		return
	}
	zones, err := h.store.Queries().ListZones(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	counts, err := h.store.Queries().ListCameraCountsByZone(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	directTotal := make(map[uuid.UUID]int64, len(counts))
	directOnline := make(map[uuid.UUID]int64, len(counts))
	for _, row := range counts {
		if row.ZoneID.Valid && scope.AllowsZone(row.ZoneID) {
			directTotal[row.ZoneID.UUID] = row.TotalCount
			directOnline[row.ZoneID.UUID] = row.OnlineCount
		}
	}

	nodes := make(map[uuid.UUID]*ZoneTreeNode, len(zones))
	for _, z := range zones {
		var parentPtr *uuid.UUID
		if z.ParentID.Valid {
			pid := z.ParentID.UUID
			parentPtr = &pid
		}
		nodes[z.ID] = &ZoneTreeNode{
			ID:                z.ID,
			ParentID:          parentPtr,
			Name:              z.Name,
			Code:              z.Code,
			ZoneType:          z.ZoneType,
			SortOrder:         z.SortOrder,
			Description:       z.Description,
			CameraCount:       directTotal[z.ID],
			OnlineCameraCount: directOnline[z.ID],
			Children:          make([]*ZoneTreeNode, 0),
		}
	}

	roots := make([]*ZoneTreeNode, 0)
	for _, z := range zones {
		node := nodes[z.ID]
		if z.ParentID.Valid {
			if parent, ok := nodes[z.ParentID.UUID]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	if !scope.Unrestricted {
		var prune func(n *ZoneTreeNode) bool
		prune = func(n *ZoneTreeNode) bool {
			kept := make([]*ZoneTreeNode, 0, len(n.Children))
			for _, ch := range n.Children {
				if prune(ch) {
					kept = append(kept, ch)
				}
			}
			n.Children = kept
			_, selfAllowed := scope.AllowedZoneSet[n.ID]
			return selfAllowed || len(kept) > 0
		}
		filteredRoots := make([]*ZoneTreeNode, 0, len(roots))
		for _, r := range roots {
			if prune(r) {
				filteredRoots = append(filteredRoots, r)
			}
		}
		roots = filteredRoots
	}

	var rollup func(n *ZoneTreeNode) (int64, int64)
	rollup = func(n *ZoneTreeNode) (int64, int64) {
		tot := n.CameraCount
		on := n.OnlineCameraCount
		for _, ch := range n.Children {
			ct, co := rollup(ch)
			tot += ct
			on += co
		}
		n.CameraCount = tot
		n.OnlineCameraCount = on
		return tot, on
	}
	for _, r := range roots {
		rollup(r)
	}

	respondOK(c, roots)
}

// Create adds a new spatial node under an optional parent zone.
func (h *ZonesHandler) Create(c *gin.Context) {
	var req zoneMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid zone payload", err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondErr(c, domain.NewInvalidArgumentError("zone name is required", nil))
		return
	}
	if !isValidZoneType(req.ZoneType) {
		respondErr(c, domain.NewInvalidArgumentError("zone_type must be factory, workshop, line, or workstation", nil))
		return
	}
	var parentID uuid.NullUUID
	if trimmed := strings.TrimSpace(req.ParentID); trimmed != "" {
		pid, err := uuid.Parse(trimmed)
		if err != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid parent_id", err))
			return
		}
		parentID = uuid.NullUUID{UUID: pid, Valid: true}
	}

	created, err := h.store.Queries().CreateZone(c.Request.Context(), sqlcgen.CreateZoneParams{
		ID:          uuid.Must(uuid.NewV7()),
		ParentID:    parentID,
		Name:        name,
		Code:        strings.TrimSpace(req.Code),
		ZoneType:    req.ZoneType,
		SortOrder:   req.SortOrder,
		Description: strings.TrimSpace(req.Description),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "zone", "created", created.ID.String())
	recordAudit(c, h.store, h.log, "zone.create", "zone", created.ID.String(), "Created zone "+created.Name, map[string]any{"zone_type": created.ZoneType})
	respondCreated(c, created)
}

// Update modifies a spatial node's metadata or parent (rejecting parent cycles).
func (h *ZonesHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid zone id", err))
		return
	}
	var req zoneMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid zone update payload", err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondErr(c, domain.NewInvalidArgumentError("zone name is required", nil))
		return
	}
	if !isValidZoneType(req.ZoneType) {
		respondErr(c, domain.NewInvalidArgumentError("zone_type must be factory, workshop, line, or workstation", nil))
		return
	}
	var parentID uuid.NullUUID
	if trimmed := strings.TrimSpace(req.ParentID); trimmed != "" {
		pid, parseErr := uuid.Parse(trimmed)
		if parseErr != nil {
			respondErr(c, domain.NewInvalidArgumentError("invalid parent_id", parseErr))
			return
		}
		if pid == id {
			respondErr(c, domain.NewInvalidArgumentError("zone cannot be its own parent", nil))
			return
		}
		descendants, descErr := collectZoneDescendants(c.Request.Context(), h.store.Queries(), id)
		if descErr != nil {
			respondErr(c, descErr)
			return
		}
		for _, d := range descendants {
			if d == pid {
				respondErr(c, domain.NewInvalidArgumentError("zone parent cannot be one of its descendants", nil))
				return
			}
		}
		parentID = uuid.NullUUID{UUID: pid, Valid: true}
	}

	updated, err := h.store.Queries().UpdateZone(c.Request.Context(), sqlcgen.UpdateZoneParams{
		ID:          id,
		ParentID:    parentID,
		Name:        name,
		Code:        strings.TrimSpace(req.Code),
		ZoneType:    req.ZoneType,
		SortOrder:   req.SortOrder,
		Description: strings.TrimSpace(req.Description),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "zone", "updated", updated.ID.String())
	notifyAllScopesChanged(c.Request.Context(), h.cache)
	recordAudit(c, h.store, h.log, "zone.update", "zone", updated.ID.String(), "Updated zone "+updated.Name, map[string]any{"zone_type": updated.ZoneType})
	respondOK(c, updated)
}

// Delete removes an empty spatial zone transactionally; returns 409 Conflict if child zones or cameras remain attached.
func (h *ZonesHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid zone id", err))
		return
	}
	ctx := c.Request.Context()
	txErr := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		childCount, qErr := q.CountChildZones(ctx, uuid.NullUUID{UUID: id, Valid: true})
		if qErr != nil {
			return qErr
		}
		if childCount > 0 {
			return domain.NewConflictError("cannot delete zone with existing child zones", nil)
		}
		camCount, qErr := q.CountCamerasInZone(ctx, uuid.NullUUID{UUID: id, Valid: true})
		if qErr != nil {
			return qErr
		}
		if camCount > 0 {
			return domain.NewConflictError("cannot delete zone with bound cameras", nil)
		}
		if delErr := q.DeleteZone(ctx, id); delErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(delErr, &pgErr) && pgErr.Code == "23503" {
				return domain.NewConflictError("cannot delete zone while dependent resources exist", delErr)
			}
			return delErr
		}
		return nil
	})
	if txErr != nil {
		respondErr(c, txErr)
		return
	}
	emitConfigEvent(ctx, h.pub, h.log, "zone", "deleted", id.String())
	notifyAllScopesChanged(ctx, h.cache)
	recordAudit(c, h.store, h.log, "zone.delete", "zone", id.String(), "Deleted zone", nil)
	respondOK(c, gin.H{"deleted": true})
}

// collectZoneDescendants returns rootID plus all recursive child zone IDs (with cycle protection).
func collectZoneDescendants(ctx context.Context, q *sqlcgen.Queries, rootID uuid.UUID) ([]uuid.UUID, error) {
	zones, err := q.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	childrenMap := make(map[uuid.UUID][]uuid.UUID)
	for _, z := range zones {
		if z.ParentID.Valid {
			childrenMap[z.ParentID.UUID] = append(childrenMap[z.ParentID.UUID], z.ID)
		}
	}
	visited := map[uuid.UUID]struct{}{rootID: {}}
	out := []uuid.UUID{rootID}
	for i := 0; i < len(out); i++ {
		cur := out[i]
		for _, ch := range childrenMap[cur] {
			if _, seen := visited[ch]; seen {
				continue
			}
			visited[ch] = struct{}{}
			out = append(out, ch)
		}
	}
	return out, nil
}
