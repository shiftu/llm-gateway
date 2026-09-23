package router

import "github.com/panda/llm-gateway/internal/store"

// SupportsProtocol uses endpoint configuration, not vendor kind: a provider
// such as OpenRouter may expose several independent protocols.
func SupportsProtocol(p store.Provider, protocol string) bool {
	switch protocol {
	case "openai":
		return p.OpenAIBaseURL != ""
	case "anthropic":
		return p.AnthropicBaseURL != ""
	case "typesafe":
		return p.TypeSafeBaseURL != ""
	case "":
		return true // protocol-agnostic routing inspection
	default:
		return false
	}
}

// ProviderHasCapability returns true when the provider has the named capability
// in the store. The value is ignored — presence alone is the gate.
// Returns false on any store error (fail-open: don't block routing).
func ProviderHasCapability(st *store.Store, providerName, capability string) bool {
	ok, _, err := st.HasCapabilityInStore(providerName, capability)
	if err != nil {
		return false
	}
	return ok
}
