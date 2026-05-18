// Package health implements provider health probing and rolling-window SLA
// tracking. The Window type records per-request latency + success samples
// and reports percentile latency and error rate over the last N samples.
//
// Concurrency is added when a failing concurrent test demands it (TDD).
package health

import (
	"sort"
	"sync"
	"time"
)

// Sample is a single recorded probe or request result.
type Sample struct {
	LatencyMs int64
	Success   bool
	At        time.Time
}

// Window is a fixed-capacity ring buffer of Samples. Once full, Record evicts
// the oldest sample. Safe for concurrent use: probe loops write while MCP
// tool handlers read.
type Window struct {
	mu      sync.Mutex
	cap     int
	samples []Sample
	// next is the index of the slot to write into; valid for ring-buffer mode
	// after the buffer first fills.
	next int
	full bool
}

// NewWindow returns a Window with the given capacity. Capacity must be >= 1.
func NewWindow(capacity int) *Window {
	if capacity < 1 {
		capacity = 1
	}
	return &Window{
		cap:     capacity,
		samples: make([]Sample, 0, capacity),
	}
}

// Record appends a sample. When the window is full, the oldest sample is
// evicted.
func (w *Window) Record(latencyMs int64, success bool, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Sample{LatencyMs: latencyMs, Success: success, At: at}
	if !w.full {
		w.samples = append(w.samples, s)
		if len(w.samples) == w.cap {
			w.full = true
			w.next = 0
		}
		return
	}
	w.samples[w.next] = s
	w.next = (w.next + 1) % w.cap
}

// Len returns the current sample count.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.samples)
}

// Percentile returns the nearest-rank p-th percentile latency in milliseconds.
// p is clamped to [1, 100]. Empty windows return 0.
//
// Nearest-rank: for p% of N samples, return the ceil(p/100 * N)th smallest
// (1-indexed) sample's latency.
func (w *Window) Percentile(p int) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.samples)
	if n == 0 {
		return 0
	}
	if p < 1 {
		p = 1
	}
	if p > 100 {
		p = 100
	}
	sorted := make([]int64, n)
	for i, s := range w.samples {
		sorted[i] = s.LatencyMs
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// ceil(p/100 * n), 1-indexed → 0-indexed: rank-1
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// ErrorRate returns the fraction of samples whose Success=false. Empty
// windows return 0.
func (w *Window) ErrorRate() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.samples)
	if n == 0 {
		return 0
	}
	errs := 0
	for _, s := range w.samples {
		if !s.Success {
			errs++
		}
	}
	return float64(errs) / float64(n)
}

// LastSample returns the most recent sample and true. If the window is empty,
// returns a zero Sample and false.
func (w *Window) LastSample() (Sample, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.samples)
	if n == 0 {
		return Sample{}, false
	}
	if !w.full {
		return w.samples[n-1], true
	}
	// In ring mode, the slot just-overwritten is samples[w.next-1] mod cap.
	idx := (w.next - 1 + w.cap) % w.cap
	return w.samples[idx], true
}
