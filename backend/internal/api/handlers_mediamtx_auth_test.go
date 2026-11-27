package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"aegisvision/backend/internal/api"
	platformredis "aegisvision/backend/internal/platform/redis"
)

type stubStreamTokenReader struct {
	tokens map[string]*platformredis.StreamTokenPayload
}

func (s *stubStreamTokenReader) GetStreamToken(_ context.Context, kind, token string) (*platformredis.StreamTokenPayload, error) {
	if p, ok := s.tokens[kind+":"+token]; ok {
		return p, nil
	}
	return nil, platformredis.ErrKeyNotFound
}

func TestMediaMTXAuthCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reader := &stubStreamTokenReader{
		tokens: map[string]*platformredis.StreamTokenPayload{
			"play:play-ok": {UserID: "u1", StreamPath: "cam_001"},
			"pull:pull-ok": {TaskID: "t1", StreamPath: "cam_001"},
		},
	}

	r := gin.New()
	r.POST("/internal/mediamtx/auth", api.NewMediaMTXAuthHandler(reader))

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{
			name: "valid_webrtc_play_token",
			body: map[string]any{"action": "read", "path": "cam_001", "protocol": "webrtc", "query": "token=play-ok"},
			want: http.StatusOK,
		},
		{
			name: "valid_rtsp_pull_token",
			body: map[string]any{"action": "read", "path": "cam_001", "protocol": "rtsp", "query": "token=pull-ok"},
			want: http.StatusOK,
		},
		{
			name: "play_token_rejected_on_rtsp",
			body: map[string]any{"action": "read", "path": "cam_001", "protocol": "rtsp", "query": "token=play-ok"},
			want: http.StatusUnauthorized,
		},
		{
			name: "mismatched_stream_path_rejected",
			body: map[string]any{"action": "read", "path": "cam_002", "protocol": "webrtc", "query": "token=play-ok"},
			want: http.StatusUnauthorized,
		},
		{
			name: "missing_token_rejected",
			body: map[string]any{"action": "read", "path": "cam_001", "protocol": "webrtc", "query": ""},
			want: http.StatusUnauthorized,
		},
		{
			name: "unknown_token_rejected",
			body: map[string]any{"action": "read", "path": "cam_001", "protocol": "rtsp", "query": "token=forged"},
			want: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/internal/mediamtx/auth", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
