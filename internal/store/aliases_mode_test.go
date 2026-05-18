package store

import (
	"testing"
)

// TestAlias_ModeDefaultsToStatic: aliases created without an explicit mode
// take the schema default of "static" — keeping v0.2 routing behavior
// unchanged for existing deployments.
func TestAlias_ModeDefaultsToStatic(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Provider FK target.
	if err := st.AddProvider(Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	// Existing 5-arg SetAlias signature — no mode parameter yet.
	if err := st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}

	a, err := st.ResolveAlias("fast")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if a.Mode != "static" {
		t.Errorf("Mode: want \"static\" (schema default), got %q", a.Mode)
	}
}

// TestSetAliasWithMode_PersistsCognitive: an alias explicitly set with
// mode="cognitive" round-trips that mode through ResolveAlias. The cognitive
// resolver in the router branches on this field.
func TestSetAliasWithMode_PersistsCognitive(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.AddProvider(Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	if err := st.SetAliasWithMode("smart", "deepseek", "deepseek-v4-pro", nil, nil, "cognitive"); err != nil {
		t.Fatalf("SetAliasWithMode: %v", err)
	}
	a, err := st.ResolveAlias("smart")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if a.Mode != "cognitive" {
		t.Errorf("Mode: want \"cognitive\", got %q", a.Mode)
	}
}
