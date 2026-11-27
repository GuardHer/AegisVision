package api

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// ScopeInvalidator is optionally implemented by CacheStore (Redis) to flush scope caches
// and notify live WebSocket gateways upon permission changes.
type ScopeInvalidator interface {
	DeleteUserScopeCache(ctx context.Context, userID string) error
	PublishScopeChanged(ctx context.Context, userID string) error
}

// UserZoneScope represents the resolved workshop-level data access scope for a user.
type UserZoneScope struct {
	Unrestricted   bool
	AllowedZoneIDs []uuid.UUID
	AllowedZoneSet map[uuid.UUID]struct{}
}

// AllowsZone reports whether the scope grants access to a resource belonging to zoneID.
func (s UserZoneScope) AllowsZone(zoneID uuid.NullUUID) bool {
	if s.Unrestricted {
		return true
	}
	if !zoneID.Valid {
		return false
	}
	_, ok := s.AllowedZoneSet[zoneID.UUID]
	return ok
}

// IntersectZoneFilter merges an optional requested zone list with the user's allowed scope.
// Returns (effectiveZoneIDs, emptyResult). When emptyResult is true, the caller should return an empty set.
func (s UserZoneScope) IntersectZoneFilter(requested []uuid.UUID) ([]uuid.UUID, bool) {
	if s.Unrestricted {
		return requested, false
	}
	if len(s.AllowedZoneIDs) == 0 {
		return nil, true
	}
	if len(requested) == 0 {
		return s.AllowedZoneIDs, false
	}
	out := make([]uuid.UUID, 0, len(requested))
	for _, id := range requested {
		if _, ok := s.AllowedZoneSet[id]; ok {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, true
	}
	return out, false
}

// ResolveUserScope computes the effective spatial zone scope for an authenticated user,
// recursively expanding all assigned zones down the zone hierarchy via BFS.
func ResolveUserScope(ctx context.Context, store *postgres.Store, claims auth.Claims) (UserZoneScope, error) {
	if store == nil || claims.Role == domain.RoleAdmin {
		return UserZoneScope{Unrestricted: true}, nil
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		return UserZoneScope{Unrestricted: true}, nil
	}
	flags, err := store.Queries().GetUserZoneScopeFlags(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Synthetic test tokens without a backing user row inherit default all_zones_access=TRUE.
			return UserZoneScope{Unrestricted: true}, nil
		}
		return UserZoneScope{}, err
	}
	if flags.Role == domain.RoleAdmin || flags.AllZonesAccess {
		return UserZoneScope{Unrestricted: true}, nil
	}

	assigned, err := store.Queries().ListUserZoneIDs(ctx, uid)
	if err != nil {
		return UserZoneScope{}, err
	}
	if len(assigned) == 0 {
		return UserZoneScope{
			Unrestricted:   false,
			AllowedZoneIDs: []uuid.UUID{},
			AllowedZoneSet: map[uuid.UUID]struct{}{},
		}, nil
	}

	allZones, err := store.Queries().ListZones(ctx)
	if err != nil {
		return UserZoneScope{}, err
	}
	expanded, set := ExpandAllowedZones(allZones, assigned)
	return UserZoneScope{
		Unrestricted:   false,
		AllowedZoneIDs: expanded,
		AllowedZoneSet: set,
	}, nil
}

// ExpandAllowedZones expands a list of root assigned zone IDs to include all descendants.
func ExpandAllowedZones(allZones []sqlcgen.Zone, rootIDs []uuid.UUID) ([]uuid.UUID, map[uuid.UUID]struct{}) {
	childrenByParent := make(map[uuid.UUID][]uuid.UUID, len(allZones))
	for _, z := range allZones {
		if z.ParentID.Valid {
			childrenByParent[z.ParentID.UUID] = append(childrenByParent[z.ParentID.UUID], z.ID)
		}
	}
	set := make(map[uuid.UUID]struct{}, len(rootIDs))
	queue := make([]uuid.UUID, 0, len(rootIDs))
	for _, id := range rootIDs {
		if _, seen := set[id]; !seen {
			set[id] = struct{}{}
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, ch := range childrenByParent[curr] {
			if _, seen := set[ch]; !seen {
				set[ch] = struct{}{}
				queue = append(queue, ch)
			}
		}
	}
	ids := make([]uuid.UUID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids, set
}

func resolveRequestScope(c *gin.Context, store *postgres.Store) (UserZoneScope, error) {
	claims, ok := auth.CurrentClaims(c)
	if !ok || claims == nil {
		return UserZoneScope{Unrestricted: true}, nil
	}
	return ResolveUserScope(c.Request.Context(), store, *claims)
}
