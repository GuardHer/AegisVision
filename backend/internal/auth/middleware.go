package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/GuardHer/AegisVision/backend/internal/domain"
)

const claimsContextKey = "aegis_auth_claims"

// TokenBlacklist checks whether an access token jti has been revoked.
type TokenBlacklist interface {
	IsTokenBlacklisted(ctx context.Context, jti string) (bool, error)
}

// ExtractBearerToken extracts a Bearer token from the HTTP Authorization header.
func ExtractBearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// AuthMiddleware validates JWT access tokens and optionally checks the Redis revocation blacklist.
func AuthMiddleware(tokens *TokenManager, blacklist TokenBlacklist) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := ExtractBearerToken(c.GetHeader("Authorization"))
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    domain.CodeUnauthorized,
				"message": "missing or invalid authorization header",
			})
			return
		}

		claims, err := tokens.ParseAccessToken(raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    domain.CodeUnauthorized,
				"message": "invalid or expired token",
			})
			return
		}

		if blacklist != nil && claims.ID != "" {
			revoked, blErr := blacklist.IsTokenBlacklisted(c.Request.Context(), claims.ID)
			if blErr != nil || revoked {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"code":    domain.CodeUnauthorized,
					"message": "token has been revoked",
				})
				return
			}
		}

		c.Set(claimsContextKey, claims)
		c.Next()
	}
}

// RequireRoles enforces that the authenticated user's role matches one of allowed roles.
func RequireRoles(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		claims, ok := CurrentClaims(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    domain.CodeUnauthorized,
				"message": "authentication required",
			})
			return
		}
		if _, permitted := allowed[claims.Role]; !permitted {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    domain.CodeForbidden,
				"message": "insufficient permissions",
			})
			return
		}
		c.Next()
	}
}

// CurrentClaims retrieves authenticated JWT claims stored by AuthMiddleware.
func CurrentClaims(c *gin.Context) (*Claims, bool) {
	val, exists := c.Get(claimsContextKey)
	if !exists {
		return nil, false
	}
	claims, ok := val.(*Claims)
	return claims, ok
}
