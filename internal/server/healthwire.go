package server

import (
	"github.com/panda/llm-gateway/internal/health"
	"github.com/panda/llm-gateway/internal/store"
)

// RegisterHealthTargets registers every store-configured provider with the
// health Manager. Probe URL = OpenAIBaseURL + "/v1/models" — universal
// across OpenAI-compat providers (DeepSeek, GLM, Qwen, Moonshot). Providers
// without an OpenAI base URL are skipped for now (v0.3 T1 scope is
// OpenAI-compat probes; Anthropic-only HEAD /v1/messages comes later).
//
// Returns the count registered. Nil store is fine — stub-mode boot just
// gets zero targets, no probing.
func RegisterHealthTargets(mgr *health.Manager, st *store.Store) (int, error) {
	if st == nil {
		return 0, nil
	}
	providers, err := st.ListProviders()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range providers {
		if p.OpenAIBaseURL == "" {
			continue
		}
		mgr.Register(p.Name, p.OpenAIBaseURL+"/v1/models", p.APIKey)
		n++
	}
	return n, nil
}
