package main

import (
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func TestSeedTypeSafeFromEnv(t *testing.T) {
	for _, endpoint := range []string{"", "https://openrouter.ai/api/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_PROVIDER_NAME", "typesafe")
			t.Setenv("LLM_GATEWAY_PROVIDER_KIND", "typesafe")
			t.Setenv("LLM_GATEWAY_PROVIDER_API_KEY", "test-key")
			t.Setenv("LLM_GATEWAY_PROVIDER_OPENAI_BASE_URL", "")
			t.Setenv("LLM_GATEWAY_PROVIDER_ANTHROPIC_BASE_URL", "")
			t.Setenv("LLM_GATEWAY_PROVIDER_TYPESAFE_BASE_URL", endpoint)
			st, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := seedProviderFromEnv(st); err != nil {
				t.Fatal(err)
			}
			if err := seedProviderFromEnv(st); err != nil {
				t.Fatal(err)
			}
			p, err := st.GetDefaultProvider()
			want := endpoint
			if want == "" {
				want = "https://api.typesafe.ai/v1"
			}
			if err != nil || p.TypeSafeBaseURL != want || p.OpenAIBaseURL != "" || p.AnthropicBaseURL != "" {
				t.Fatalf("seed: %+v %v", p, err)
			}
		})
	}
}
