package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/platform/telemetry"
)

// ScopeResolver resolves a user's workshop/camera access scope at WebSocket connect time.
type ScopeResolver func(ctx context.Context, userID, role string) (ClientScope, error)

// ServerDeps holds explicit dependencies for the WebSocket HTTP server.
type ServerDeps struct {
	Hub           *Hub
	Tokens        *auth.TokenManager
	Blacklist     auth.TokenBlacklist
	ScopeResolver ScopeResolver
	Health        *telemetry.Health
	Logger        *slog.Logger
}

// Server serves authenticated browser WebSocket connections at /ws/realtime.
type Server struct {
	hub           *Hub
	tokens        *auth.TokenManager
	blacklist     auth.TokenBlacklist
	scopeResolver ScopeResolver
	health        *telemetry.Health
	log           *slog.Logger
}

// NewServer creates a WebSocket gateway Server.
func NewServer(deps ServerDeps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Server{
		hub:           deps.Hub,
		tokens:        deps.Tokens,
		blacklist:     deps.Blacklist,
		scopeResolver: deps.ScopeResolver,
		health:        deps.Health,
		log:           deps.Logger,
	}
}

// Handler returns the HTTP handler exposing /ws/realtime and health probes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.health != nil {
		h := s.health.Handler()
		mux.Handle("/healthz", h)
		mux.Handle("/readyz", h)
		mux.Handle("/metrics", h)
	}
	mux.HandleFunc("/ws/realtime", s.handleRealtime)
	return mux
}

type clientMessage struct {
	Type      string   `json:"type"`
	CameraIDs []string `json:"camera_ids"`
}

func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authenticate(r)
	if !ok {
		http.Error(w, `{"code":40101,"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	scope := ClientScope{UserID: claims.UserID, Unrestricted: true}
	if s.scopeResolver != nil {
		resolved, err := s.scopeResolver(r.Context(), claims.UserID, claims.Role)
		if err != nil {
			s.log.Warn("resolve ws user scope failed; failing closed", "user_id", claims.UserID, "err", err)
			scope = ClientScope{UserID: claims.UserID, Unrestricted: false}
		} else {
			resolved.UserID = claims.UserID
			scope = resolved
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin/CORS enforced by Nginx reverse proxy
	})
	if err != nil {
		s.log.Warn("websocket accept failed", "err", err)
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	clientID := uuid.Must(uuid.NewV7()).String()
	client := s.hub.RegisterWithScope(clientID, scope)
	defer s.hub.Unregister(clientID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Dedicated writer goroutine honoring alarm priority over frame detections.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		s.writeLoop(ctx, conn, client)
	}()

	// Reader loop handles client subscribe commands and detects disconnect.
	for {
		_, data, readErr := conn.Read(ctx)
		if readErr != nil {
			cancel()
			<-writeDone
			return
		}
		var msg clientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "subscribe" {
			s.hub.SetSubscriptions(clientID, msg.CameraIDs)
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, conn *websocket.Conn, client *Client) {
	pingTick := time.NewTicker(25 * time.Second)
	defer pingTick.Stop()

	writePayload := func(payload []byte) bool {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(writeCtx, websocket.MessageText, payload) == nil
	}

	for {
		// Priority check: always drain alarm_alert before frame_detections.
		select {
		case alert := <-client.AlarmChan():
			if !writePayload(alert) {
				return
			}
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return
		case alert := <-client.AlarmChan():
			if !writePayload(alert) {
				return
			}
		case frame := <-client.FrameChan():
			if !writePayload(frame) {
				return
			}
		case <-pingTick.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) authenticate(r *http.Request) (auth.Claims, bool) {
	if s.tokens == nil {
		return auth.Claims{}, false
	}
	raw := auth.ExtractBearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	if raw == "" {
		return auth.Claims{}, false
	}
	claims, err := s.tokens.ParseAccessToken(raw)
	if err != nil {
		return auth.Claims{}, false
	}
	if s.blacklist != nil && claims.ID != "" {
		revoked, blErr := s.blacklist.IsTokenBlacklisted(r.Context(), claims.ID)
		if blErr != nil || revoked {
			return auth.Claims{}, false
		}
	}
	return *claims, true
}
