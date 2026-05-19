package health

import (
	"context"
	"sync"
	"time"
)

// ProbeFn is the callable signature of Probe. The Manager takes it as an
// injectable dependency so tests can supply a fake instead of standing up an
// httptest server for every assertion.
type ProbeFn func(ctx context.Context, url, token string) ProbeResult

// Snapshot is the observable state of a provider's health at one point in
// time. JSON tags are the agent-facing contract (returned by the
// get_provider_health MCP tool); keep snake_case.
type Snapshot struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SampleCount   int    `json:"sample_count"`
	LastHealthy   bool   `json:"last_healthy"`
	LastLatencyMs int64  `json:"last_latency_ms"`
}

// targetConf holds the probe URL and auth token for one provider.
type targetConf struct {
	url   string
	token string
}

// Manager tracks a set of provider endpoints and exposes their current health
// state. Concurrency: Register may be called from MCP handlers while Run is
// looping, and Snapshot may be called from any goroutine — the mu RWMutex
// guards the maps. Probe HTTP calls happen outside the lock so a slow probe
// can't block Register.
type Manager struct {
	probe    ProbeFn
	interval time.Duration

	mu      sync.RWMutex
	targets map[string]targetConf // name → {url, token}
	windows map[string]*Window
}

// NewManager builds a Manager with the given probe function and tick
// interval. interval is stored for the background loop (later) — Bootstrap
// itself runs once synchronously.
func NewManager(probe ProbeFn, interval time.Duration) *Manager {
	return &Manager{
		probe:    probe,
		interval: interval,
		targets:  make(map[string]targetConf),
		windows:  make(map[string]*Window),
	}
}

// Register adds (or replaces) a provider target. token is sent as
// "Authorization: Bearer <token>" on every probe request.
func (m *Manager) Register(name, url, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets[name] = targetConf{url: url, token: token}
	if _, ok := m.windows[name]; !ok {
		m.windows[name] = NewWindow(128)
	}
}

// snapshotTargets returns a slice of (name, url, token, window) tuples taken
// under RLock so the probe loop can release the manager lock before issuing
// slow HTTP calls. Window itself is internally thread-safe.
type targetEntry struct {
	name  string
	url   string
	token string
	w     *Window
}

func (m *Manager) snapshotTargets() []targetEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]targetEntry, 0, len(m.targets))
	for name, conf := range m.targets {
		out = append(out, targetEntry{name: name, url: conf.url, token: conf.token, w: m.windows[name]})
	}
	return out
}

// Bootstrap probes every registered provider once, sequentially, and records
// each result in that provider's window. This is the Q11 startup gate: the
// caller blocks on Bootstrap before flipping /healthz to 200.
func (m *Manager) Bootstrap(ctx context.Context) error {
	for _, t := range m.snapshotTargets() {
		res := m.probe(ctx, t.url, t.token)
		t.w.Record(res.LatencyMs, res.Healthy, time.Now())
	}
	return nil
}

// Run is the background probe loop. On each tick of m.interval it re-probes
// every registered provider sequentially. Exits cleanly when ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, e := range m.snapshotTargets() {
				res := m.probe(ctx, e.url, e.token)
				e.w.Record(res.LatencyMs, res.Healthy, time.Now())
			}
		}
	}
}

// Snapshot returns the current health state of a provider, or ok=false if the
// name is unknown.
func (m *Manager) Snapshot(name string) (Snapshot, bool) {
	m.mu.RLock()
	conf, ok := m.targets[name]
	w := m.windows[name]
	m.mu.RUnlock()
	if !ok {
		return Snapshot{}, false
	}
	snap := Snapshot{Name: name, URL: conf.url, SampleCount: w.Len()}
	if last, ok := w.LastSample(); ok {
		snap.LastHealthy = last.Success
		snap.LastLatencyMs = last.LatencyMs
	}
	return snap, true
}
