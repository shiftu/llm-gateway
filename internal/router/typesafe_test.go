package router

import (
	"errors"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func TestCognitiveRoutingFiltersProtocols(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []store.Provider{
		{Name: "chat", Kind: "openai", OpenAIBaseURL: "https://example.com/v1", APIKey: "k"},
		{Name: "eval", Kind: "typesafe", TypeSafeBaseURL: "https://api.typesafe.ai/v1", APIKey: "k"},
	} {
		if err := st.AddProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetAliasWithMode("smart", "chat", "model", nil, nil, "cognitive"); err != nil {
		t.Fatal(err)
	}
	r := NewWithScorer(st, func(p store.Provider) ScoreInputs { return ScoreInputs{Quality: 1, Healthy: true} })
	for protocol, want := range map[string]string{"openai": "chat", "typesafe": "eval"} {
		route, err := r.ResolveForTeamProtocol("smart", "", protocol)
		if err != nil || route.Provider.Name != want || len(route.Cognitive.Candidates) != 1 {
			t.Fatalf("%s: %+v %v", protocol, route, err)
		}
	}
	if _, err := r.ResolveForTeamProtocol("smart", "", "anthropic"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("unsupported protocol: %v", err)
	}
}
