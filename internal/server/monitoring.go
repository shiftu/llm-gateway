package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// monitoring holds healthz + optional Prometheus /metrics state.
type monitoring struct {
	enableMetrics bool
	startTime     time.Time
	requests      atomic.Int64 // total proxy requests
	ready         atomic.Bool  // Q11 startup gate: flipped after Bootstrap returns
}

func newMonitoring() *monitoring {
	return &monitoring{
		enableMetrics: os.Getenv("LLM_GATEWAY_ENABLE_METRICS") == "1",
		startTime:     time.Now(),
	}
}

// mountMonitoring registers /healthz and (if enabled) /metrics on mux.
// These routes are intentionally NOT behind auth.Middleware so that
// Kubernetes liveness probes and Prometheus scrapers work without a token.
func (s *Server) mountMonitoring() {
	m := newMonitoring()
	s.monitor = m
	s.mux.HandleFunc("/healthz", m.handleHealthz)
	if m.enableMetrics {
		s.mux.HandleFunc("/metrics", m.handleMetrics)
	}
}

// handleHealthz reports gateway readiness. Returns 503 + status=starting
// while the Q11 startup probe sweep is still running, 200 + status=ok once
// MarkReady has been called. Body always includes uptime so orchestrators
// can detect stuck-starting processes.
func (m *monitoring) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := "starting"
	code := http.StatusServiceUnavailable
	if m.ready.Load() {
		status = "ok"
		code = http.StatusOK
	}
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":         status,
		"uptime_seconds": int(time.Since(m.startTime).Seconds()),
	})
}

// MarkReady flips the readiness flag. Called by the start command after
// Manager.Bootstrap completes (or its 10s deadline elapses).
func (m *monitoring) MarkReady() {
	if m != nil {
		m.ready.Store(true)
	}
}

// handleMetrics emits a minimal Prometheus text-format exposition.
// Cardinality is deliberately low: no team label (plan Q3 decision).
// Users who need per-team metrics can add relabel_config on the Prometheus side.
func (m *monitoring) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP llm_gateway_up Whether the gateway is running (1=up).\n")
	fmt.Fprintf(w, "# TYPE llm_gateway_up gauge\n")
	fmt.Fprintf(w, "llm_gateway_up 1\n\n")

	fmt.Fprintf(w, "# HELP llm_gateway_requests_total Total proxied LLM requests.\n")
	fmt.Fprintf(w, "# TYPE llm_gateway_requests_total counter\n")
	fmt.Fprintf(w, "llm_gateway_requests_total %d\n\n", m.requests.Load())

	fmt.Fprintf(w, "# HELP llm_gateway_uptime_seconds Gateway uptime in seconds.\n")
	fmt.Fprintf(w, "# TYPE llm_gateway_uptime_seconds gauge\n")
	fmt.Fprintf(w, "llm_gateway_uptime_seconds %d\n", int(time.Since(m.startTime).Seconds()))
}

// IncRequests increments the request counter. Called from routeAndForward.
func (m *monitoring) IncRequests() {
	if m != nil {
		m.requests.Add(1)
	}
}
