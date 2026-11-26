package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	platformredis "github.com/GuardHer/AegisVision/backend/internal/platform/redis"
)

// StreamTokenReader looks up MediaMTX play/pull authorization tokens in Redis.
type StreamTokenReader interface {
	GetStreamToken(ctx context.Context, kind, token string) (*platformredis.StreamTokenPayload, error)
}

type mediamtxAuthRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	Path     string `json:"path"`
	Protocol string `json:"protocol"`
	ID       string `json:"id"`
	Query    string `json:"query"`
}

// NewMediaMTXAuthHandler returns the Gin handler for MediaMTX external HTTP authentication.
// This internal endpoint validates short-lived WebRTC play tokens and Runtime RTSP pull tokens.
func NewMediaMTXAuthHandler(tokens StreamTokenReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req mediamtxAuthRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Status(http.StatusUnauthorized)
			return
		}

		// Only read/playback actions are authorized via short-lived stream tokens.
		if req.Action != "read" && req.Action != "playback" {
			c.Status(http.StatusUnauthorized)
			return
		}

		token := extractQueryToken(req.Query)
		if token == "" {
			c.Status(http.StatusUnauthorized)
			return
		}

		ctx := c.Request.Context()
		kinds := tokenKindsForProtocol(req.Protocol)
		for _, kind := range kinds {
			payload, err := tokens.GetStreamToken(ctx, kind, token)
			if err == nil && payload != nil && payload.StreamPath == req.Path {
				c.Status(http.StatusOK)
				return
			}
		}

		c.Status(http.StatusUnauthorized)
	}
}

func extractQueryToken(rawQuery string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(rawQuery), "?")
	if trimmed == "" {
		return ""
	}
	vals, err := url.ParseQuery(trimmed)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(vals.Get("token"))
}

func tokenKindsForProtocol(protocol string) []string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "webrtc":
		return []string{platformredis.StreamTokenKindPlay}
	case "rtsp":
		return []string{platformredis.StreamTokenKindPull, platformredis.StreamTokenKindPlay}
	default:
		return []string{platformredis.StreamTokenKindPlay, platformredis.StreamTokenKindPull}
	}
}
