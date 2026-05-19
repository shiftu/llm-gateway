package health

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestManager_BootstrapProbesRegistered: After registering a provider and
// running Bootstrap (sync sweep, one probe per provider), Snapshot reflects
// the recorded sample. This is the Q11 startup-gate behavior in miniature:
// one synchronous probe, result observable immediately.
func TestManager_BootstrapProbesRegistered(t *testing.T) {
	probeCalls := 0
	var lastURL string
	fakeProbe := func(_ context.Context, url, _ string) ProbeResult {
		probeCalls++
		lastURL = url
		return ProbeResult{Healthy: true, LatencyMs: 42, StatusCode: 200}
	}

	m := NewManager(fakeProbe, 100*time.Millisecond)
	m.Register("deepseek", "https://api.deepseek.com/v1/models", "")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if probeCalls != 1 {
		t.Errorf("probeCalls: want 1, got %d", probeCalls)
	}
	if lastURL != "https://api.deepseek.com/v1/models" {
		t.Errorf("probe url: want deepseek /v1/models, got %q", lastURL)
	}

	snap, ok := m.Snapshot("deepseek")
	if !ok {
		t.Fatalf("Snapshot(deepseek): want ok=true (provider was registered)")
	}
	if snap.SampleCount != 1 {
		t.Errorf("SampleCount: want 1, got %d", snap.SampleCount)
	}
	if !snap.LastHealthy {
		t.Errorf("LastHealthy: want true")
	}
	if snap.LastLatencyMs != 42 {
		t.Errorf("LastLatencyMs: want 42, got %d", snap.LastLatencyMs)
	}
}

// TestManager_RunRecordsOnTick: Run is the background probe loop. While the
// context is alive it re-probes every registered provider at the configured
// interval, accumulating samples. When the context is cancelled it must exit
// cleanly (no leaked goroutine).
func TestManager_RunRecordsOnTick(t *testing.T) {
	var probeCount atomic.Int64
	fakeProbe := func(_ context.Context, _, _ string) ProbeResult {
		probeCount.Add(1)
		return ProbeResult{Healthy: true, LatencyMs: 5}
	}
	m := NewManager(fakeProbe, 20*time.Millisecond)
	m.Register("p1", "http://example/", "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	// Let a handful of ticks fire (~7 ticks at 20ms in 150ms).
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancel — goroutine leaked")
	}

	if got := probeCount.Load(); got < 3 {
		t.Errorf("probe calls during Run: want >= 3, got %d", got)
	}
	snap, _ := m.Snapshot("p1")
	if snap.SampleCount < 3 {
		t.Errorf("SampleCount after Run: want >= 3, got %d", snap.SampleCount)
	}
}

// TestManager_ConcurrentRegisterWithRun: Register may be called while Run is
// iterating providers — e.g. an `add_provider` MCP call lands while the
// server is up. Concurrent map iteration + write panics in Go's runtime, and
// a plain read alongside a write trips the race detector. This test pins
// the manager's concurrency contract.
func TestManager_ConcurrentRegisterWithRun(t *testing.T) {
	fakeProbe := func(_ context.Context, _, _ string) ProbeResult {
		return ProbeResult{Healthy: true, LatencyMs: 1}
	}
	m := NewManager(fakeProbe, 1*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	// Register a stream of providers while Run iterates the map.
	for i := 0; i < 200; i++ {
		m.Register(fmt.Sprintf("p%d", i), "http://example/", "")
	}
	// Also exercise Snapshot concurrently — a read against the same maps.
	for i := 0; i < 50; i++ {
		_, _ = m.Snapshot(fmt.Sprintf("p%d", i))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}
