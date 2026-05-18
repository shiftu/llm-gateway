package router

import (
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// fakeProviders / fakeScorer is a hand-rolled stub of ScoreInputsResolver
// that returns a fixed ScoreInputs per provider name. Lets the router test
// stay independent of store costs + health Manager wiring.
type fakeScorer map[string]ScoreInputs

func (f fakeScorer) Inputs(p store.Provider) ScoreInputs {
	if in, ok := f[p.Name]; ok {
		return in
	}
	return ScoreInputs{Healthy: false}
}

// TestCognitiveResolver_PicksHighestScorer: with three providers and a
// cost-heavy weight profile, the resolver picks the cheapest healthy one.
// Demonstrates the core T3 contract — score-and-pick, not just lookup.
func TestCognitiveResolver_PicksHighestScorer(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	mustAddProvider(t, st, "deepseek", "deepseek")
	mustAddProvider(t, st, "glm", "glm")
	mustAddProvider(t, st, "qwen", "qwen")
	if err := st.SetAliasWithMode("smart", "deepseek", "smart-model", nil, nil, "cognitive"); err != nil {
		t.Fatalf("SetAliasWithMode: %v", err)
	}

	// glm is cheapest, qwen mid, deepseek most expensive — all healthy with
	// the same latency. Cost-heavy weights → glm wins.
	scorer := fakeScorer{
		"deepseek": {InputCostUSD1k: 0.01, OutputCostUSD1k: 0.02, P99LatencyMs: 100, Quality: 1, Healthy: true},
		"glm":      {InputCostUSD1k: 0.001, OutputCostUSD1k: 0.002, P99LatencyMs: 100, Quality: 1, Healthy: true},
		"qwen":     {InputCostUSD1k: 0.005, OutputCostUSD1k: 0.01, P99LatencyMs: 100, Quality: 1, Healthy: true},
	}
	r := NewWithScorer(st, scorer.Inputs)

	route, err := r.ResolveForTeam("smart", "")
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "glm" {
		t.Errorf("Provider: want glm (cheapest), got %q", route.Provider.Name)
	}
	if route.UpstreamModel != "smart-model" {
		t.Errorf("UpstreamModel: want smart-model (from alias), got %q", route.UpstreamModel)
	}
	if route.ViaAlias != "smart" {
		t.Errorf("ViaAlias: want smart, got %q", route.ViaAlias)
	}
	// Cognitive routing must record a non-empty breakdown so T5 can render it.
	if len(route.Cognitive.Candidates) == 0 {
		t.Errorf("Cognitive.Candidates: want non-empty breakdown for trace, got empty")
	}
}

// TestCognitiveResolver_SkipsUnhealthy: a higher-cost healthy provider beats
// a cheap unhealthy one (unhealthy scores 0 per T2 acceptance).
func TestCognitiveResolver_SkipsUnhealthy(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	mustAddProvider(t, st, "primary", "deepseek")
	mustAddProvider(t, st, "backup", "deepseek")
	if err := st.SetAliasWithMode("smart", "primary", "smart-model", nil, nil, "cognitive"); err != nil {
		t.Fatalf("SetAliasWithMode: %v", err)
	}

	scorer := fakeScorer{
		"primary": {InputCostUSD1k: 0.001, OutputCostUSD1k: 0.001, P99LatencyMs: 50, Quality: 1, Healthy: false},
		"backup":  {InputCostUSD1k: 0.01, OutputCostUSD1k: 0.01, P99LatencyMs: 200, Quality: 1, Healthy: true},
	}
	r := NewWithScorer(st, scorer.Inputs)
	route, err := r.ResolveForTeam("smart", "")
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "backup" {
		t.Errorf("Provider: want backup (only healthy), got %q", route.Provider.Name)
	}
}

// TestCognitiveResolver_StaticUnchanged: aliases without explicit cognitive
// mode route exactly the same way as v0.2 — regression guard.
func TestCognitiveResolver_StaticUnchanged(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	mustAddProvider(t, st, "deepseek", "deepseek")
	mustAddProvider(t, st, "glm", "glm")
	if err := st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}

	// Scorer would prefer glm if cognitive — but mode is static so it's
	// ignored.
	scorer := fakeScorer{
		"deepseek": {InputCostUSD1k: 0.1, Healthy: true},
		"glm":      {InputCostUSD1k: 0.0001, Healthy: true},
	}
	r := NewWithScorer(st, scorer.Inputs)
	route, err := r.ResolveForTeam("fast", "")
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "deepseek" {
		t.Errorf("static alias must ignore scorer: want deepseek, got %q", route.Provider.Name)
	}
	if route.UpstreamModel != "deepseek-v4-flash" {
		t.Errorf("static alias must keep upstream model: got %q", route.UpstreamModel)
	}
	if route.Cognitive.Candidates != nil {
		t.Errorf("static alias must not populate cognitive trace, got %d candidates", len(route.Cognitive.Candidates))
	}
}

func mustAddProvider(t *testing.T, st *store.Store, name, kind string) {
	t.Helper()
	err := st.AddProvider(store.Provider{
		Name: name, Kind: kind,
		OpenAIBaseURL: "https://api." + name + "/v1",
		APIKey:        "k-" + name,
	})
	if err != nil {
		t.Fatalf("AddProvider(%s): %v", name, err)
	}
}
