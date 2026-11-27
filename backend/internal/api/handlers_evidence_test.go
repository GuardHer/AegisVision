package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/internal/platform/storage"
)

type stubAlarmEvidenceReader struct {
	alarms map[uuid.UUID]sqlcgen.Alarm
}

func (s *stubAlarmEvidenceReader) GetAlarmByID(_ context.Context, id uuid.UUID) (sqlcgen.Alarm, error) {
	a, ok := s.alarms[id]
	if !ok {
		return sqlcgen.Alarm{}, domain.NewNotFoundError("alarm not found", nil)
	}
	return a, nil
}

// Review Focus #4: unauthorized / expired / tampered evidence URLs must return 401;
// valid signed requests with Range header must return 206 Partial Content with accurate Content-Range.
func TestEvidenceProxySecurityAndRange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	localStore, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal(): %v", err)
	}

	alarmID := uuid.Must(uuid.NewV7())
	videoData := bytes.Repeat([]byte("0123456789"), 200) // 2000 bytes
	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'J', 'P', 'G', 0xFF, 0xD9}

	if err := localStore.PutObject(context.Background(), "videos/test/a1.mp4", bytes.NewReader(videoData), int64(len(videoData)), "video/mp4"); err != nil {
		t.Fatalf("PutObject(video): %v", err)
	}
	if err := localStore.PutObject(context.Background(), "images/test/a1.jpg", bytes.NewReader(imageData), int64(len(imageData)), "image/jpeg"); err != nil {
		t.Fatalf("PutObject(image): %v", err)
	}

	alarms := &stubAlarmEvidenceReader{
		alarms: map[uuid.UUID]sqlcgen.Alarm{
			alarmID: {
				ID:             alarmID,
				ImageObjectKey: "images/test/a1.jpg",
				VideoObjectKey: "videos/test/a1.mp4",
			},
		},
	}

	tm := auth.NewTokenManager("jwt-secret-key-at-least-16-chars", time.Hour)
	signer := auth.NewEvidenceSigner("evidence-sign-secret-1234567890")
	h := api.NewEvidenceHandler(alarms, localStore, tm, nil, signer)

	r := gin.New()
	r.GET("/api/v1/evidence/:alarm_id/image", h.ServeImage)
	r.GET("/api/v1/evidence/:alarm_id/video", h.ServeVideo)

	// 1. No token and no signature -> 401
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+alarmID.String()+"/video", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned request status = %d, want 401", w.Code)
	}

	// 2. Expired signature -> 401
	expiredURL := signer.SignEvidenceURLAt("http://localhost", alarmID.String(), "video", -time.Minute, time.Now())
	uExp, _ := url.Parse(expiredURL)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, uExp.RequestURI(), nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired signature status = %d, want 401", w.Code)
	}

	// 3. Tampered alarm_id -> 401
	validURL := signer.SignEvidenceURL("http://localhost", alarmID.String(), "video", time.Hour)
	uValid, _ := url.Parse(validURL)
	otherAlarmID := uuid.Must(uuid.NewV7())
	tamperedURI := "/api/v1/evidence/" + otherAlarmID.String() + "/video?" + uValid.RawQuery
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tamperedURI, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered alarm_id status = %d, want 401", w.Code)
	}

	// 4. Valid signature + Range: bytes=0-1023 -> 206 Partial Content
	reqRange := httptest.NewRequest(http.MethodGet, uValid.RequestURI(), nil)
	reqRange.Header.Set("Range", "bytes=0-1023")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, reqRange)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("valid range status = %d, want 206 (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Range"); got != "bytes 0-1023/2000" {
		t.Errorf("Content-Range = %q, want bytes 0-1023/2000", got)
	}
	if w.Body.Len() != 1024 {
		t.Errorf("body len = %d, want 1024", w.Body.Len())
	}

	// 5. Valid JWT Bearer token for image -> 200 OK
	jwtTok, _, _, _ := tm.IssueAccessToken("u-viewer", "viewer", domain.RoleViewer)
	reqJWT := httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+alarmID.String()+"/image", nil)
	reqJWT.Header.Set("Authorization", "Bearer "+jwtTok)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, reqJWT)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), imageData) {
		t.Fatalf("JWT image fetch = (status=%d, len=%d), want (200, %d)", w.Code, w.Body.Len(), len(imageData))
	}
}
