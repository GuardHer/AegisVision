package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := auth.HashPassword("StrongPass!2026")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	ok, err := auth.VerifyPassword("StrongPass!2026", hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = (%v, %v), want (true, nil)", ok, err)
	}
	okWrong, err := auth.VerifyPassword("WrongPass", hash)
	if err != nil || okWrong {
		t.Fatalf("VerifyPassword(wrong) = (%v, %v), want (false, nil)", okWrong, err)
	}
}

func TestEvidenceSignatureSignAndVerify(t *testing.T) {
	signer := auth.NewEvidenceSigner("evidence-hmac-secret-1234567890")
	now := time.Unix(1795700000, 0)

	signedURL := signer.SignEvidenceURLAt("http://192.168.0.114", "alarm-001", "video", time.Hour, now)
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	exp := parsed.Query().Get("exp")
	sig := parsed.Query().Get("sig")
	if exp == "" || sig == "" {
		t.Fatalf("expected exp and sig query params in %q", signedURL)
	}

	if !signer.VerifyEvidenceSignature("alarm-001", "video", exp, sig, now.Add(30*time.Minute)) {
		t.Fatal("expected valid signature to verify before expiry")
	}
	if signer.VerifyEvidenceSignature("alarm-002", "video", exp, sig, now.Add(30*time.Minute)) {
		t.Fatal("expected tampered alarm_id to fail verification")
	}
	if signer.VerifyEvidenceSignature("alarm-001", "image", exp, sig, now.Add(30*time.Minute)) {
		t.Fatal("expected tampered kind to fail verification")
	}
	if signer.VerifyEvidenceSignature("alarm-001", "video", exp, sig, now.Add(2*time.Hour)) {
		t.Fatal("expected expired signature to fail verification")
	}
}

type noopBlacklist struct {
	revoked map[string]bool
}

func (b *noopBlacklist) IsTokenBlacklisted(_ context.Context, jti string) (bool, error) {
	return b.revoked[jti], nil
}

func TestRBACMiddleware(t *testing.T) {
	tm := auth.NewTokenManager("jwt-secret-key-at-least-16-chars", time.Hour)
	bl := &noopBlacklist{revoked: map[string]bool{}}

	r := gin.New()
	r.Use(auth.AuthMiddleware(tm, bl))
	r.GET("/monitor", auth.RequireRoles(domain.RoleAdmin, domain.RoleOperator, domain.RoleViewer), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.POST("/alarms/handle", auth.RequireRoles(domain.RoleAdmin, domain.RoleOperator), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.POST("/cameras", auth.RequireRoles(domain.RoleAdmin), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	tokAdmin, _, _, _ := tm.IssueAccessToken("u-admin", "admin", domain.RoleAdmin)
	tokOp, _, _, _ := tm.IssueAccessToken("u-op", "op1", domain.RoleOperator)
	tokViewer, jtiViewer, _, _ := tm.IssueAccessToken("u-view", "viewer1", domain.RoleViewer)

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"unauthenticated", http.MethodGet, "/monitor", "", http.StatusUnauthorized},
		{"viewer_monitor_ok", http.MethodGet, "/monitor", tokViewer, http.StatusOK},
		{"viewer_handle_forbidden", http.MethodPost, "/alarms/handle", tokViewer, http.StatusForbidden},
		{"viewer_cameras_forbidden", http.MethodPost, "/cameras", tokViewer, http.StatusForbidden},
		{"operator_handle_ok", http.MethodPost, "/alarms/handle", tokOp, http.StatusOK},
		{"operator_cameras_forbidden", http.MethodPost, "/cameras", tokOp, http.StatusForbidden},
		{"admin_cameras_ok", http.MethodPost, "/cameras", tokAdmin, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s %s status = %d, want %d (body=%s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
			}
		})
	}

	// Revoked token must return 401
	bl.revoked[jtiViewer] = true
	req := httptest.NewRequest(http.MethodGet, "/monitor", nil)
	req.Header.Set("Authorization", "Bearer "+tokViewer)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token status = %d, want 401", w.Code)
	}
}
