package server

import (
	"strings"

	"github.com/panda/llm-gateway/internal/health"
	"github.com/panda/llm-gateway/internal/store"
)

// RegisterHealthTargets registers every store-configured provider with the
// health Manager. Probe URL = OpenAIBaseURL + "/models" — callers store the
// full base URL (including any /v1 prefix) per OpenAI SDK convention, so the
// gateway appends only the resource name. Providers without an OpenAI base
// URL are skipped (Anthropic-only HEAD /v1/messages comes in a later cycle).
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
		mgr.Register(p.Name, strings.TrimRight(p.OpenAIBaseURL, "/")+"/models", p.APIKey)
		n++
	}
	return n, nil
}
