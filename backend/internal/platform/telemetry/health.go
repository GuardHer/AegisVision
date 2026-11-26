package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// CheckFunc verifies a single dependency's readiness.
type CheckFunc func(ctx context.Context) error

// Health exposes /healthz, /readyz, and /metrics HTTP handlers for platform processes.
type Health struct {
	service   string
	startedAt time.Time
	ready     atomic.Bool

	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewHealth creates a Health probe controller (initially not ready until SetReady(true)).
func NewHealth(service string) *Health {
	return &Health{
		service:   service,
		startedAt: time.Now(),
		checks:    make(map[string]CheckFunc),
	}
}

// SetReady marks the service ready or unready (call SetReady(false) first during graceful shutdown).
func (h *Health) SetReady(ready bool) {
	h.ready.Store(ready)
}

// AddReadinessCheck registers a named dependency check invoked on /readyz.
func (h *Health) AddReadinessCheck(name string, fn CheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = fn
}

// Handler returns an http.Handler serving /healthz, /readyz, and /metrics.
func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/metrics", h.handleMetrics)
	return mux
}

func (h *Health) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"service": h.service,
	})
}

func (h *Health) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !h.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "not_ready",
			"service": h.service,
		})
		return
	}

	h.mu.RLock()
	checks := make(map[string]CheckFunc, len(h.checks))
	for k, v := range h.checks {
		checks[k] = v
	}
	h.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	failures := make(map[string]string)
	for name, fn := range checks {
		if err := fn(ctx); err != nil {
			failures[name] = err.Error()
		}
	}

	if len(failures) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "unhealthy",
			"service":  h.service,
			"failures": failures,
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ready",
		"service": h.service,
	})
}

func (h *Health) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	readyVal := 0
	if h.ready.Load() {
		readyVal = 1
	}
	_, _ = fmt.Fprintf(w,
		"# HELP aegis_service_ready Whether the service is ready to serve traffic (1=ready, 0=not ready)\n"+
			"# TYPE aegis_service_ready gauge\n"+
			"aegis_service_ready{service=%q} %d\n"+
			"# HELP aegis_service_uptime_seconds Process uptime in seconds\n"+
			"# TYPE aegis_service_uptime_seconds gauge\n"+
			"aegis_service_uptime_seconds{service=%q} %.2f\n",
		h.service, readyVal, h.service, time.Since(h.startedAt).Seconds(),
	)
}
