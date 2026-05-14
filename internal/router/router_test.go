package router

import (
	"errors"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// router_test.go covers plan §4 Task 5 + F-2/F-3/F-11.
//
// Resolution priority:
//  1. Alias hit → (alias.provider, alias.upstream_model, via_alias=<client_model>)
//  2. Default provider set → (default, client_model, via_alias="")
//  3. Neither → ErrNoRoute
//
// F-11: an orphaned alias (provider deleted, FK cascade skipped via SQL
// trickery in tests) returns ErrNoRoute with a diagnostic wrapper.

func newStoreWithProvider(t *testing.T, name string, isDefault bool) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AddProvider(store.Provider{
		Name: name, Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "k", IsDefault: isDefault,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolve_AliasHit(t *testing.T) {
	s := newStoreWithProvider(t, "deepseek", false)
	_ = s.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)

	r := New(s)
	route, err := r.Resolve("fast")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if route.Provider.Name != "deepseek" {
		t.Errorf("Provider: %s", route.Provider.Name)
	}
	if route.UpstreamModel != "deepseek-v4-flash" {
		t.Errorf("UpstreamModel: %s", route.UpstreamModel)
	}
	if route.ViaAlias != "fast" {
		t.Errorf("ViaAlias: want fast, got %q", route.ViaAlias)
	}
}

func TestResolve_DefaultPassThrough(t *testing.T) {
	s := newStoreWithProvider(t, "deepseek", true)

	r := New(s)
	route, err := r.Resolve("any-unknown-model-id")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if route.Provider.Name != "deepseek" {
		t.Errorf("Provider: %s", route.Provider.Name)
	}
	// Default route preserves the client-requested model verbatim
	if route.UpstreamModel != "any-unknown-model-id" {
		t.Errorf("UpstreamModel: want pass-through, got %q", route.UpstreamModel)
	}
	if route.ViaAlias != "" {
		t.Errorf("ViaAlias must be empty for default route, got %q", route.ViaAlias)
	}
}

func TestResolve_NoAliasNoDefault_ErrNoRoute(t *testing.T) {
	s := newStoreWithProvider(t, "deepseek", false) // not default

	r := New(s)
	_, err := r.Resolve("whatever")
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("want ErrNoRoute, got %v", err)
	}
}

func TestResolve_EmptyStore_ErrNoRoute(t *testing.T) {
	s, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = s.Close() })

	r := New(s)
	_, err := r.Resolve("anything")
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("want ErrNoRoute, got %v", err)
	}
}

func TestResolve_AliasOverridesDefault(t *testing.T) {
	// When both an alias and a default exist, alias wins. This prevents
	// surprises where someone adds an alias and the default still claims it.
	s := newStoreWithProvider(t, "deepseek", true)
	_ = s.AddProvider(store.Provider{
		Name: "glm", Kind: "glm",
		OpenAIBaseURL: "https://open.bigmodel.cn",
		APIKey:        "k",
	})
	_ = s.SetAlias("smart", "glm", "glm-4-plus", nil, nil)

	r := New(s)
	route, _ := r.Resolve("smart")
	if route.Provider.Name != "glm" {
		t.Errorf("alias did not override default: got %s", route.Provider.Name)
	}
	if route.UpstreamModel != "glm-4-plus" {
		t.Errorf("UpstreamModel: %s", route.UpstreamModel)
	}
}

// --- ResolveForTeam tests (T18) ---

func newStoreWithTeam(t *testing.T) (*store.Store, store.Team) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	team, err := s.AddTeam("acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	return s, team
}

func TestResolveForTeam_TeamAliasBeatsGlobal(t *testing.T) {
	s, team := newStoreWithTeam(t)
	_ = s.AddProvider(store.Provider{Name: "global-p", Kind: "deepseek", OpenAIBaseURL: "u1", APIKey: "k"})
	_ = s.AddProvider(store.Provider{Name: "team-p", Kind: "glm", OpenAIBaseURL: "u2", APIKey: "k"})
	_ = s.SetAlias("fast", "global-p", "global-model", nil, nil)
	_ = s.SetAliasForTeam("fast", team.ID, "team-p", "team-model", nil, nil)

	r := New(s)
	route, err := r.ResolveForTeam("fast", team.ID)
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "team-p" || route.UpstreamModel != "team-model" {
		t.Errorf("team alias not selected: provider=%s model=%s", route.Provider.Name, route.UpstreamModel)
	}
}

func TestResolveForTeam_FallsBackToGlobalAlias(t *testing.T) {
	s, team := newStoreWithTeam(t)
	_ = s.AddProvider(store.Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)
	// No team-scoped alias for "fast"

	r := New(s)
	route, err := r.ResolveForTeam("fast", team.ID)
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "deepseek" || route.UpstreamModel != "deepseek-v4-flash" {
		t.Errorf("global alias fallback failed: provider=%s model=%s", route.Provider.Name, route.UpstreamModel)
	}
}

func TestResolveForTeam_FallsBackToDefaultProvider(t *testing.T) {
	s, team := newStoreWithTeam(t)
	_ = s.AddProvider(store.Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k", IsDefault: true})
	// No aliases at all

	r := New(s)
	route, err := r.ResolveForTeam("some-model", team.ID)
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "deepseek" || route.UpstreamModel != "some-model" {
		t.Errorf("default provider fallback failed: provider=%s model=%s", route.Provider.Name, route.UpstreamModel)
	}
}

func TestResolveForTeam_EmptyTeamID_GlobalOnly(t *testing.T) {
	// Empty teamID is the legacy-token path — team aliases must not interfere.
	s, team := newStoreWithTeam(t)
	_ = s.AddProvider(store.Provider{Name: "global-p", Kind: "deepseek", OpenAIBaseURL: "u1", APIKey: "k"})
	_ = s.AddProvider(store.Provider{Name: "team-p", Kind: "glm", OpenAIBaseURL: "u2", APIKey: "k"})
	_ = s.SetAlias("fast", "global-p", "global-model", nil, nil)
	_ = s.SetAliasForTeam("fast", team.ID, "team-p", "team-model", nil, nil)

	r := New(s)
	route, err := r.ResolveForTeam("fast", "")
	if err != nil {
		t.Fatalf("ResolveForTeam: %v", err)
	}
	if route.Provider.Name != "global-p" {
		t.Errorf("empty teamID must use global alias, got provider %q", route.Provider.Name)
	}
}

func TestResolveForTeam_NoRoute(t *testing.T) {
	s, team := newStoreWithTeam(t)
	_ = s.AddProvider(store.Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	// No aliases, no default provider

	r := New(s)
	_, err := r.ResolveForTeam("model", team.ID)
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("want ErrNoRoute, got %v", err)
	}
}

// F-11 simulation: alias rows that survive a force-removed provider. Real
// schema cascades on delete; we manufacture the orphan by direct SQL to
// confirm the router fails gracefully rather than panicking.
func TestResolve_OrphanedAlias_ErrNoRoute(t *testing.T) {
	s := newStoreWithProvider(t, "deepseek", false)
	_ = s.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)
	// Drop the FK cascade by manually deleting only the provider row using
	// foreign_keys=off — this mirrors a corrupted state from manual SQL edit
	// or restored backup.
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM providers WHERE name = ?`, "deepseek"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	r := New(s)
	_, err := r.Resolve("fast")
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("orphan alias must surface ErrNoRoute, got %v", err)
	}
}
