package health

import (
	"sync"
	"testing"
	"time"
)

// TestWindow_RecordAndPercentile is the core behavior of the rolling SLA window:
// it records latency samples and reports percentiles over the last-N window.
//
// Sample data: 100 successful samples with latencies 1..100ms.
// Expected p99 = 99 (the 99th value in a sorted 1..100 series).
// Using nearest-rank percentile: p% of N samples → ceil(p/100 * N)th sample (1-indexed).
// p99 of N=100 → ceil(0.99 * 100) = 99 → samples[98] in 0-indexed sorted slice → 99.
func TestWindow_RecordAndPercentile(t *testing.T) {
	w := NewWindow(100)
	base := time.Unix(1700000000, 0)
	for i := 1; i <= 100; i++ {
		w.Record(int64(i), true, base.Add(time.Duration(i)*time.Second))
	}
	if got := w.Len(); got != 100 {
		t.Fatalf("Len after 100 records: want 100, got %d", got)
	}
	if got := w.Percentile(99); got != 99 {
		t.Errorf("Percentile(99): want 99, got %d", got)
	}
	if got := w.Percentile(50); got != 50 {
		t.Errorf("Percentile(50): want 50, got %d", got)
	}
	if got := w.ErrorRate(); got != 0 {
		t.Errorf("ErrorRate (all successes): want 0, got %v", got)
	}
}

// TestWindow_Eviction verifies that recording beyond capacity drops the oldest
// samples (ring-buffer semantics).
func TestWindow_Eviction(t *testing.T) {
	w := NewWindow(3)
	base := time.Unix(1700000000, 0)
	// Record 5 samples into a capacity-3 window. Only samples 3, 4, 5 should remain.
	for i := 1; i <= 5; i++ {
		w.Record(int64(i*10), true, base.Add(time.Duration(i)*time.Second))
	}
	if got := w.Len(); got != 3 {
		t.Fatalf("Len after 5 records into cap-3: want 3, got %d", got)
	}
	// p50 of {30, 40, 50} → 40 by nearest-rank (ceil(0.5*3) = 2 → 0-indexed 1 → 40)
	if got := w.Percentile(50); got != 40 {
		t.Errorf("Percentile(50) after eviction: want 40, got %d", got)
	}
	// p100 → 50 (max of remaining)
	if got := w.Percentile(100); got != 50 {
		t.Errorf("Percentile(100) after eviction: want 50, got %d", got)
	}
}

// TestWindow_ErrorRate verifies error-rate math over a mix of successes
// and failures.
func TestWindow_ErrorRate(t *testing.T) {
	w := NewWindow(10)
	base := time.Unix(1700000000, 0)
	// 3 successes, 7 failures → error rate 0.7
	for i := 0; i < 10; i++ {
		w.Record(50, i >= 7, base) // first 7 fail, last 3 succeed
	}
	got := w.ErrorRate()
	want := 0.7
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ErrorRate: want %v, got %v", want, got)
	}
}

// TestWindow_LastSample returns the most recently recorded sample, even after
// the ring buffer has wrapped.
func TestWindow_LastSample(t *testing.T) {
	w := NewWindow(3)
	base := time.Unix(1700000000, 0)
	// Empty window: no last sample.
	if _, ok := w.LastSample(); ok {
		t.Errorf("LastSample on empty window: want ok=false")
	}
	// Record 4 samples into cap-3. Last should be sample 4.
	for i := 1; i <= 4; i++ {
		w.Record(int64(i), i%2 == 0, base.Add(time.Duration(i)*time.Second))
	}
	s, ok := w.LastSample()
	if !ok {
		t.Fatalf("LastSample after 4 records: want ok=true")
	}
	if s.LatencyMs != 4 {
		t.Errorf("LastSample.LatencyMs: want 4, got %d", s.LatencyMs)
	}
	if !s.Success {
		t.Errorf("LastSample.Success: want true (sample 4 is even), got false")
	}
	wantAt := base.Add(4 * time.Second)
	if !s.At.Equal(wantAt) {
		t.Errorf("LastSample.At: want %v, got %v", wantAt, s.At)
	}
}

// TestWindow_ConcurrentAccess exercises the real production access pattern:
// one goroutine continuously Records while a second goroutine reads
// Percentile + LastSample + ErrorRate. Run with `go test -race` to detect
// the data race on shared internal state.
func TestWindow_ConcurrentAccess(t *testing.T) {
	w := NewWindow(64)
	base := time.Unix(1700000000, 0)
	const iters = 5000

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine: continuously Record.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			w.Record(int64(i%200), i%4 != 0, base.Add(time.Duration(i)*time.Millisecond))
		}
	}()

	// Reader goroutine: continuously read.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = w.Percentile(99)
			_ = w.ErrorRate()
			_, _ = w.LastSample()
		}
	}()

	wg.Wait()
	// Final invariants — exact values are non-deterministic under concurrency,
	// but the window must be full and the error rate must be in [0,1].
	if got := w.Len(); got != 64 {
		t.Errorf("final Len: want 64, got %d", got)
	}
	if r := w.ErrorRate(); r < 0 || r > 1 {
		t.Errorf("final ErrorRate out of [0,1]: %v", r)
	}
}
