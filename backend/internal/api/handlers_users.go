package api

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// UsersHandler manages user accounts, RBAC roles, and workshop zone data scopes (admin only).
type UsersHandler struct {
	store *postgres.Store
	cache CacheStore
}

// NewUsersHandler constructs a UsersHandler.
func NewUsersHandler(store *postgres.Store, cache CacheStore) *UsersHandler {
	return &UsersHandler{store: store, cache: cache}
}

// BootstrapDefaultAdmin ensures an initial admin user exists when the users table is empty.
func BootstrapDefaultAdmin(ctx context.Context, store *postgres.Store, username, password string) (bool, error) {
	count, err := store.Queries().CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, fmt.Errorf("hash bootstrap admin password: %w", err)
	}
	if _, err := store.Queries().CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:           uuid.Must(uuid.NewV7()),
		Username:     username,
		PasswordHash: hash,
		DisplayName:  "Administrator",
		Role:         domain.RoleAdmin,
		Enabled:      true,
	}); err != nil {
		return false, fmt.Errorf("create bootstrap admin: %w", err)
	}
	return true, nil
}

func isValidRole(role string) bool {
	return role == domain.RoleAdmin || role == domain.RoleOperator || role == domain.RoleViewer
}

type createUserRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required,min=8"`
	DisplayName string `json:"display_name" binding:"required"`
	Role        string `json:"role" binding:"required"`
	Enabled     *bool  `json:"enabled"`
}

type updateUserRequest struct {
	DisplayName string `json:"display_name" binding:"required"`
	Role        string `json:"role" binding:"required"`
	Enabled     bool   `json:"enabled"`
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required,min=8"`
}

// List returns all user profiles (without password hashes).
func (h *UsersHandler) List(c *gin.Context) {
	users, err := h.store.Queries().ListUsers(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	out := make([]userProfile, 0, len(users))
	for _, u := range users {
		out = append(out, toUserProfile(u))
	}
	respondOK(c, out)
}

// Create inserts a new user with Argon2id hashed password.
func (h *UsersHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user payload", err))
		return
	}
	if !isValidRole(req.Role) {
		respondErr(c, domain.NewInvalidArgumentError("role must be admin, operator, or viewer", nil))
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to hash password", err))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	u, err := h.store.Queries().CreateUser(c.Request.Context(), sqlcgen.CreateUserParams{
		ID:           uuid.Must(uuid.NewV7()),
		Username:     req.Username,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
		Role:         req.Role,
		Enabled:      enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondCreated(c, toUserProfile(u))
}

// Update modifies a user's display name, role, and enabled state.
func (h *UsersHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user id", err))
		return
	}
	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid update payload", err))
		return
	}
	if !isValidRole(req.Role) {
		respondErr(c, domain.NewInvalidArgumentError("role must be admin, operator, or viewer", nil))
		return
	}

	u, err := h.store.Queries().UpdateUser(c.Request.Context(), sqlcgen.UpdateUserParams{
		ID:          id,
		DisplayName: req.DisplayName,
		Role:        req.Role,
		Enabled:     req.Enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toUserProfile(u))
}

// Delete removes a user account.
func (h *UsersHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user id", err))
		return
	}
	if err := h.store.Queries().DeleteUser(c.Request.Context(), id); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}

// ResetPassword replaces a user's password hash and revokes existing refresh tokens.
func (h *UsersHandler) ResetPassword(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user id", err))
		return
	}
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("new_password must be at least 8 characters", err))
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to hash password", err))
		return
	}

	ctx := c.Request.Context()
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{
			ID:           id,
			PasswordHash: hash,
		}); err != nil {
			return err
		}
		return q.RevokeAllUserRefreshTokens(ctx, id)
	}); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"reset": true})
}

type updateUserZonesRequest struct {
	AllZonesAccess bool     `json:"all_zones_access"`
	ZoneIDs        []string `json:"zone_ids"`
}

// GetZones returns a user's workshop access flag and explicitly assigned zone IDs.
func (h *UsersHandler) GetZones(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user id", err))
		return
	}
	ctx := c.Request.Context()
	flags, err := h.store.Queries().GetUserZoneScopeFlags(ctx, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	zoneIDs, err := h.store.Queries().ListUserZoneIDs(ctx, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if zoneIDs == nil {
		zoneIDs = []uuid.UUID{}
	}
	respondOK(c, gin.H{
		"user_id":          id,
		"all_zones_access": flags.AllZonesAccess,
		"zone_ids":         zoneIDs,
	})
}

// UpdateZones replaces a user's workshop access scope, invalidates Redis cache, and notifies aegis-ws immediately.
func (h *UsersHandler) UpdateZones(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user id", err))
		return
	}
	var req updateUserZonesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid user zones payload", err))
		return
	}
	zoneIDs, err := parseUUIDSlice(req.ZoneIDs)
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid zone_ids", err))
		return
	}

	ctx := c.Request.Context()
	if _, err := h.store.Queries().GetUserZoneScopeFlags(ctx, id); err != nil {
		respondErr(c, err)
		return
	}
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if uErr := q.UpdateUserAllZonesAccess(ctx, sqlcgen.UpdateUserAllZonesAccessParams{
			ID:             id,
			AllZonesAccess: req.AllZonesAccess,
		}); uErr != nil {
			return uErr
		}
		if dErr := q.DeleteUserZonesByUser(ctx, id); dErr != nil {
			return dErr
		}
		for _, zid := range zoneIDs {
			if iErr := q.InsertUserZone(ctx, sqlcgen.InsertUserZoneParams{
				UserID: id,
				ZoneID: zid,
			}); iErr != nil {
				return iErr
			}
		}
		return nil
	}); err != nil {
		respondErr(c, err)
		return
	}

	if inv, ok := h.cache.(ScopeInvalidator); ok && inv != nil {
		_ = inv.DeleteUserScopeCache(ctx, id.String())
		_ = inv.PublishScopeChanged(ctx, id.String())
	}

	respondOK(c, gin.H{
		"user_id":          id,
		"all_zones_access": req.AllZonesAccess,
		"zone_ids":         zoneIDs,
	})
}
