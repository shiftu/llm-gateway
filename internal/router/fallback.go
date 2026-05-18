package router

import "github.com/panda/llm-gateway/internal/store"

// NextFallbackProvider returns the next provider name to try given a fallback
// policy, the cognitive trace from the original dispatch, and the set of
// providers already tried. Returns "" when the chain is exhausted (depth limit
// reached or no untried candidates remain).
func NextFallbackProvider(policy store.FallbackPolicy, trace CognitiveTrace, tried map[string]struct{}) string {
	if len(tried) >= policy.MaxChainDepth {
		return ""
	}
	switch policy.Action {
	case "specific_provider":
		if _, already := tried[policy.TargetProvider]; already {
			return ""
		}
		return policy.TargetProvider
	default: // next_best
		for _, c := range trace.Candidates {
			if _, already := tried[c.ProviderName]; !already {
				return c.ProviderName
			}
		}
		return ""
	}
}
