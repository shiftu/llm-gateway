package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	s := NewServer("test-token", nil)
	// Need to bypass auth for healthz — mountMonitoring already registered in NewServer
	handler := s.mux // raw mux, no auth middleware

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", resp["status"])
	}
	uptime, ok := resp["uptime_seconds"].(float64)
	if !ok || uptime < 0 {
		t.Fatalf("expected uptime_seconds >= 0, got %v", resp["uptime_seconds"])
	}
}

func TestMetrics_DisabledByDefault(t *testing.T) {
	os.Unsetenv("LLM_GATEWAY_ENABLE_METRICS")
	s := NewServer("test-token", nil)
	handler := s.mux

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should 404 when metrics disabled
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when metrics disabled, got %d", w.Code)
	}
}

func TestMetrics_Enabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ENABLE_METRICS", "1")
	s := NewServer("test-token", nil)
	handler := s.mux

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "llm_gateway_up 1") {
		t.Fatalf("expected llm_gateway_up metric, got:\n%s", body)
	}
	if !strings.Contains(body, "llm_gateway_requests_total 0") {
		t.Fatalf("expected requests_total 0, got:\n%s", body)
	}
	if !strings.Contains(body, "llm_gateway_uptime_seconds") {
		t.Fatalf("expected uptime_seconds metric, got:\n%s", body)
	}
}

func TestIncRequests(t *testing.T) {
	m := newMonitoring()
	if m.requests.Load() != 0 {
		t.Fatal("expected 0 initial requests")
	}
	m.IncRequests()
	if m.requests.Load() != 1 {
		t.Fatalf("expected 1, got %d", m.requests.Load())
	}
}

func TestIncRequests_NilReceiver(t *testing.T) {
	var m *monitoring
	m.IncRequests() // should not panic
}

func TestMonitoringStartTime(t *testing.T) {
	m := newMonitoring()
	if m.startTime.IsZero() {
		t.Fatal("startTime should be set")
	}
	if time.Since(m.startTime) > 5*time.Second {
		t.Fatal("startTime should be recent")
	}
}
