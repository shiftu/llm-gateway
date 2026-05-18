package router

import (
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func makeCands(names ...string) []Candidate {
	cands := make([]Candidate, len(names))
	for i, n := range names {
		cands[i] = Candidate{ProviderName: n, Breakdown: Breakdown{Total: float64(len(names) - i)}}
	}
	return cands
}

func tried(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func TestNextFallbackProvider_NextBest(t *testing.T) {
	policy := store.FallbackPolicy{Action: "next_best", MaxChainDepth: 3}
	trace := CognitiveTrace{Candidates: makeCands("alpha", "beta", "gamma")}

	got := NextFallbackProvider(policy, trace, tried("alpha"))
	if got != "beta" {
		t.Errorf("want beta, got %q", got)
	}
}

func TestNextFallbackProvider_NextBest_SkipsMultiple(t *testing.T) {
	policy := store.FallbackPolicy{Action: "next_best", MaxChainDepth: 3}
	trace := CognitiveTrace{Candidates: makeCands("alpha", "beta", "gamma")}

	got := NextFallbackProvider(policy, trace, tried("alpha", "beta"))
	if got != "gamma" {
		t.Errorf("want gamma, got %q", got)
	}
}

func TestNextFallbackProvider_NextBest_AllTried(t *testing.T) {
	policy := store.FallbackPolicy{Action: "next_best", MaxChainDepth: 3}
	trace := CognitiveTrace{Candidates: makeCands("alpha", "beta", "gamma")}

	got := NextFallbackProvider(policy, trace, tried("alpha", "beta", "gamma"))
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestNextFallbackProvider_SpecificProvider(t *testing.T) {
	policy := store.FallbackPolicy{Action: "specific_provider", TargetProvider: "backup", MaxChainDepth: 3}
	trace := CognitiveTrace{Candidates: makeCands("alpha", "beta")}

	got := NextFallbackProvider(policy, trace, tried("alpha"))
	if got != "backup" {
		t.Errorf("want backup, got %q", got)
	}
}

func TestNextFallbackProvider_SpecificProvider_AlreadyTried(t *testing.T) {
	policy := store.FallbackPolicy{Action: "specific_provider", TargetProvider: "backup", MaxChainDepth: 3}
	trace := CognitiveTrace{Candidates: makeCands("alpha")}

	got := NextFallbackProvider(policy, trace, tried("alpha", "backup"))
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestNextFallbackProvider_ChainDepthExhausted(t *testing.T) {
	policy := store.FallbackPolicy{Action: "next_best", MaxChainDepth: 2}
	trace := CognitiveTrace{Candidates: makeCands("alpha", "beta", "gamma")}

	// tried already has 2 entries == MaxChainDepth
	got := NextFallbackProvider(policy, trace, tried("alpha", "beta"))
	if got != "" {
		t.Errorf("want empty when chain exhausted, got %q", got)
	}
}
