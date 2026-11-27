package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// AuthCache abstracts Redis token blacklist operations needed by auth endpoints.
type AuthCache interface {
	BlacklistToken(ctx context.Context, jti string, ttl time.Duration) error
	IsTokenBlacklisted(ctx context.Context, jti string) (bool, error)
}

// AuthHandler serves /api/v1/auth/* endpoints.
type AuthHandler struct {
	store           *postgres.Store
	tokens          *auth.TokenManager
	cache           AuthCache
	refreshTokenTTL time.Duration
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(store *postgres.Store, tokens *auth.TokenManager, cache AuthCache, refreshTTL time.Duration) *AuthHandler {
	if refreshTTL <= 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	return &AuthHandler{
		store:           store,
		tokens:          tokens,
		cache:           cache,
		refreshTokenTTL: refreshTTL,
	}
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type userProfile struct {
	ID             uuid.UUID  `json:"id"`
	Username       string     `json:"username"`
	DisplayName    string     `json:"display_name"`
	Role           string     `json:"role"`
	Enabled        bool       `json:"enabled"`
	AllZonesAccess bool       `json:"all_zones_access"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
}

func toUserProfile(u sqlcgen.User) userProfile {
	return userProfile{
		ID:             u.ID,
		Username:       u.Username,
		DisplayName:    u.DisplayName,
		Role:           u.Role,
		Enabled:        u.Enabled,
		AllZonesAccess: u.AllZonesAccess,
		LastLoginAt:    u.LastLoginAt,
	}
}

// Login authenticates a user and returns JWT access + opaque refresh tokens.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("username and password are required", err))
		return
	}

	ctx := c.Request.Context()
	u, err := h.store.Queries().GetUserByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			respondErr(c, domain.NewUnauthorizedError("invalid username or password", err))
			return
		}
		respondErr(c, err)
		return
	}
	if !u.Enabled {
		respondErr(c, domain.NewForbiddenError("user account is disabled", nil))
		return
	}

	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		respondErr(c, domain.NewUnauthorizedError("invalid username or password", err))
		return
	}

	accessTok, _, expAt, err := h.tokens.IssueAccessToken(u.ID.String(), u.Username, u.Role)
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to issue access token", err))
		return
	}
	rawRefresh, err := auth.GenerateOpaqueToken()
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to issue refresh token", err))
		return
	}

	refreshExp := time.Now().UTC().Add(h.refreshTokenTTL)
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.CreateRefreshToken(ctx, sqlcgen.CreateRefreshTokenParams{
			ID:        uuid.Must(uuid.NewV7()),
			UserID:    u.ID,
			TokenHash: auth.HashRefreshToken(rawRefresh),
			ExpiresAt: refreshExp,
		}); err != nil {
			return err
		}
		return q.UpdateUserLastLogin(ctx, u.ID)
	}); err != nil {
		respondErr(c, err)
		return
	}

	respondOK(c, gin.H{
		"access_token":  accessTok,
		"refresh_token": rawRefresh,
		"expires_at":    expAt,
		"user":          toUserProfile(u),
	})
}

// Refresh rotates a valid refresh token and issues a fresh access token atomically inside a transaction.
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("refresh_token is required", err))
		return
	}

	ctx := c.Request.Context()
	tokenHash := auth.HashRefreshToken(req.RefreshToken)
	newRawRefresh, err := auth.GenerateOpaqueToken()
	if err != nil {
		respondErr(c, domain.NewInternalError("failed to issue refresh token", err))
		return
	}

	var (
		u         sqlcgen.User
		accessTok string
		expAt     time.Time
	)
	if err := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		rt, txErr := q.GetRefreshTokenByHash(ctx, tokenHash)
		if txErr != nil {
			return domain.NewUnauthorizedError("invalid refresh token", txErr)
		}
		if rt.RevokedAt != nil || rt.ExpiresAt.Before(time.Now().UTC()) {
			return domain.NewUnauthorizedError("refresh token expired or revoked", nil)
		}

		u, txErr = q.GetUserByID(ctx, rt.UserID)
		if txErr != nil || !u.Enabled {
			return domain.NewUnauthorizedError("user disabled or missing", txErr)
		}

		accessTok, _, expAt, txErr = h.tokens.IssueAccessToken(u.ID.String(), u.Username, u.Role)
		if txErr != nil {
			return domain.NewInternalError("failed to issue access token", txErr)
		}

		if txErr = q.RevokeRefreshToken(ctx, tokenHash); txErr != nil {
			return txErr
		}
		_, txErr = q.CreateRefreshToken(ctx, sqlcgen.CreateRefreshTokenParams{
			ID:        uuid.Must(uuid.NewV7()),
			UserID:    u.ID,
			TokenHash: auth.HashRefreshToken(newRawRefresh),
			ExpiresAt: time.Now().UTC().Add(h.refreshTokenTTL),
		})
		return txErr
	}); err != nil {
		respondErr(c, err)
		return
	}

	respondOK(c, gin.H{
		"access_token":  accessTok,
		"refresh_token": newRawRefresh,
		"expires_at":    expAt,
		"user":          toUserProfile(u),
	})
}

// Logout revokes the current access token (via Redis blacklist) and optional refresh token.
func (h *AuthHandler) Logout(c *gin.Context) {
	ctx := c.Request.Context()
	if claims, ok := auth.CurrentClaims(c); ok && h.cache != nil && claims.ID != "" && claims.ExpiresAt != nil {
		remaining := time.Until(claims.ExpiresAt.Time)
		if remaining > 0 {
			_ = h.cache.BlacklistToken(ctx, claims.ID, remaining)
		}
	}

	var req logoutRequest
	if err := c.ShouldBindJSON(&req); err == nil && req.RefreshToken != "" {
		_ = h.store.Queries().RevokeRefreshToken(ctx, auth.HashRefreshToken(req.RefreshToken))
	}

	c.JSON(http.StatusOK, Envelope{Code: domain.CodeOK, Message: "ok"})
}

// Me returns the currently authenticated user's profile.
func (h *AuthHandler) Me(c *gin.Context) {
	claims, ok := auth.CurrentClaims(c)
	if !ok {
		respondErr(c, domain.NewUnauthorizedError("authentication required", nil))
		return
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		respondErr(c, domain.NewUnauthorizedError("invalid user id in token", err))
		return
	}
	u, err := h.store.Queries().GetUserByID(c.Request.Context(), uid)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toUserProfile(u))
}
