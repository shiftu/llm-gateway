package server

import (
	"context"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/health"
	"github.com/panda/llm-gateway/internal/store"
)

// TestRegisterHealthTargets: each provider with an OpenAI base URL becomes a
// registered health target whose probe URL is base + /v1/models — the
// universal OpenAI-compat readiness endpoint. Providers with no OpenAI URL
// are skipped (Anthropic-only providers handled in a later cycle).
func TestRegisterHealthTargets(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	mustAdd(t, st, store.Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k1"})
	mustAdd(t, st, store.Provider{Name: "glm", Kind: "glm", OpenAIBaseURL: "https://open.bigmodel.cn/api/paas", APIKey: "k2"})
	mustAdd(t, st, store.Provider{Name: "anthr-only", Kind: "anthropic", AnthropicBaseURL: "https://api.anthropic.com", APIKey: "k3"})

	var probed []string
	fakeProbe := func(_ context.Context, url string) health.ProbeResult {
		probed = append(probed, url)
		return health.ProbeResult{Healthy: true, LatencyMs: 1}
	}
	mgr := health.NewManager(fakeProbe, time.Minute)

	n, err := RegisterHealthTargets(mgr, st)
	if err != nil {
		t.Fatalf("RegisterHealthTargets: %v", err)
	}
	if n != 2 {
		t.Errorf("registered count: want 2 (deepseek + glm; anthr-only skipped), got %d", n)
	}

	// Drive one bootstrap sweep so we can read back the URLs that got probed.
	if err := mgr.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if len(probed) != 2 {
		t.Fatalf("probed URLs: want 2, got %d (%v)", len(probed), probed)
	}
	wantURLs := map[string]bool{
		"https://api.deepseek.com/v1/models":           true,
		"https://open.bigmodel.cn/api/paas/v1/models": true,
	}
	for _, u := range probed {
		if !wantURLs[u] {
			t.Errorf("unexpected probe URL %q (want one of %v)", u, wantURLs)
		}
	}
}

// TestRegisterHealthTargets_NilStore: stub-mode boot (no store) registers
// nothing and returns no error — health probe is just disabled.
func TestRegisterHealthTargets_NilStore(t *testing.T) {
	mgr := health.NewManager(func(_ context.Context, _ string) health.ProbeResult {
		return health.ProbeResult{}
	}, time.Minute)
	n, err := RegisterHealthTargets(mgr, nil)
	if err != nil {
		t.Fatalf("RegisterHealthTargets(nil): %v", err)
	}
	if n != 0 {
		t.Errorf("nil-store count: want 0, got %d", n)
	}
}

func mustAdd(t *testing.T, st *store.Store, p store.Provider) {
	t.Helper()
	if err := st.AddProvider(p); err != nil {
		t.Fatalf("AddProvider(%s): %v", p.Name, err)
	}
}
