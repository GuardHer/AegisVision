package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GuardHer/AegisVision/backend/internal/platform/telemetry"
)

func TestHealthAndReadinessProbes(t *testing.T) {
	h := telemetry.NewHealth("aegis-api")

	// /healthz always 200
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", w.Code)
	}

	// /readyz starts unready (503) until marked ready
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial /readyz status = %d, want 503", w.Code)
	}

	// Register failing check -> still 503 even if SetReady(true)
	failing := true
	h.AddReadinessCheck("postgres", func(_ context.Context) error {
		if failing {
			return errors.New("db down")
		}
		return nil
	})
	h.SetReady(true)
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with failing check status = %d, want 503", w.Code)
	}

	// Dependency healthy -> 200
	failing = false
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/readyz when healthy status = %d, want 200", w.Code)
	}

	// Shutdown transitions /readyz back to 503
	h.SetReady(false)
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz after SetReady(false) status = %d, want 503", w.Code)
	}
}
