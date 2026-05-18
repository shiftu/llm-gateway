package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbe_Healthy: a 200 OK response classifies as Healthy with a measured
// latency and the upstream status code.
func TestProbe_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	r := Probe(ctx, srv.URL)
	if !r.Healthy {
		t.Errorf("Healthy: want true, got false (err=%v, status=%d)", r.Err, r.StatusCode)
	}
	if r.StatusCode != 200 {
		t.Errorf("StatusCode: want 200, got %d", r.StatusCode)
	}
	if r.Err != nil {
		t.Errorf("Err: want nil, got %v", r.Err)
	}
	if r.LatencyMs < 0 {
		t.Errorf("LatencyMs: want >= 0, got %d", r.LatencyMs)
	}
}

// TestProbe_Unhealthy5xx: server returns 503 → Healthy=false, StatusCode=503,
// Err=nil (the transport succeeded; the upstream just rejected us).
func TestProbe_Unhealthy5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	r := Probe(ctx, srv.URL)
	if r.Healthy {
		t.Errorf("Healthy: want false (503 is not 2xx), got true")
	}
	if r.StatusCode != 503 {
		t.Errorf("StatusCode: want 503, got %d", r.StatusCode)
	}
	if r.Err != nil {
		t.Errorf("Err: want nil (transport succeeded), got %v", r.Err)
	}
}

// TestProbe_Timeout: server hangs longer than the context deadline → Err is
// set, Healthy=false. The latency should be close to the deadline, not the
// server's full hang duration.
func TestProbe_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r := Probe(ctx, srv.URL)
	if r.Healthy {
		t.Errorf("Healthy: want false (timeout), got true")
	}
	if r.Err == nil {
		t.Errorf("Err: want non-nil (timeout), got nil")
	}
	// Latency should reflect the deadline (~100ms), not the server hang (~2s).
	if r.LatencyMs > 1000 {
		t.Errorf("LatencyMs: want close to deadline (~100ms), got %d (probe didn't honor context)", r.LatencyMs)
	}
}

// TestProbe_BadURL: an unreachable URL produces an Err and Healthy=false. No
// crash, no panic.
func TestProbe_BadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Reserved TEST-NET-1 (RFC 5737), no listener, dropped or unreachable.
	r := Probe(ctx, "http://192.0.2.1:1/")
	if r.Healthy {
		t.Errorf("Healthy: want false (unreachable), got true")
	}
	if r.Err == nil {
		t.Errorf("Err: want non-nil (unreachable), got nil")
	}
}
