package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func isSensitiveAuditKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(k, "password") ||
		strings.Contains(k, "secret") ||
		strings.Contains(k, "token") ||
		strings.Contains(k, "authorization") ||
		strings.Contains(k, "api_key") ||
		strings.Contains(k, "private_key")
}

// SanitizeAuditDetail recursively strips sensitive credential/secret/token keys from audit metadata.
func SanitizeAuditDetail(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSensitiveAuditKey(k) {
			continue
		}
		out[k] = sanitizeAuditValue(v)
	}
	return out
}

func sanitizeAuditValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return SanitizeAuditDetail(tv)
	case []any:
		res := make([]any, 0, len(tv))
		for _, item := range tv {
			res = append(res, sanitizeAuditValue(item))
		}
		return res
	case json.RawMessage:
		var parsed any
		if len(tv) > 0 && json.Unmarshal(tv, &parsed) == nil {
			return sanitizeAuditValue(parsed)
		}
		return nil
	default:
		return v
	}
}

// recordAudit writes a sanitized audit trail row without ever failing or blocking the primary API operation.
func recordAudit(
	c *gin.Context,
	store *postgres.Store,
	log *slog.Logger,
	action, resourceType, resourceID, summary string,
	detail map[string]any,
) {
	if store == nil || c == nil {
		return
	}
	var actorID uuid.NullUUID
	actorUsername := ""
	if claims, ok := auth.CurrentClaims(c); ok && claims != nil {
		actorUsername = claims.Username
		if uid, err := uuid.Parse(claims.UserID); err == nil {
			actorID = uuid.NullUUID{UUID: uid, Valid: true}
		}
	}
	sanitized := SanitizeAuditDetail(detail)
	detailJSON, err := json.Marshal(sanitized)
	if err != nil || len(detailJSON) == 0 {
		detailJSON = []byte(`{}`)
	}

	reqID := c.Writer.Header().Get("X-Request-Id")
	if reqID == "" {
		reqID = c.GetHeader("X-Request-Id")
	}

	params := sqlcgen.CreateAuditLogParams{
		ID:            uuid.Must(uuid.NewV7()),
		ActorUserID:   actorID,
		ActorUsername: actorUsername,
		Action:        action,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Summary:       summary,
		Detail:        detailJSON,
		ClientIp:      c.ClientIP(),
		RequestID:     reqID,
	}
	if _, err := store.Queries().CreateAuditLog(c.Request.Context(), params); err != nil {
		// If actor_user_id was deleted while JWT remained valid, fall back to NULL actor_user_id so audit record is preserved.
		var pgErr *pgconn.PgError
		if actorID.Valid && errors.As(err, &pgErr) && pgErr.Code == "23503" {
			params.ActorUserID = uuid.NullUUID{}
			if _, retryErr := store.Queries().CreateAuditLog(c.Request.Context(), params); retryErr == nil {
				return
			}
		}
		if log != nil {
			log.Warn("write audit log failed", "action", action, "resource_type", resourceType, "err", err)
		}
	}
}
