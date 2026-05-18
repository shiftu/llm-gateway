package router

import "github.com/panda/llm-gateway/internal/store"

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
