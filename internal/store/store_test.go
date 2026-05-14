package store

import (
	"path/filepath"
	"testing"
	"time"
)

// store_test.go covers Task 4: SQLite schema + providers/aliases/logs CRUD.
// Per plan F-9 we verify versioned migrations are idempotent (re-applying on
// a populated DB must not destroy data). Per F-4 the DSN must enable WAL +
// busy_timeout + foreign_keys so cascades work and writers don't deadlock.

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_AppliesSchemaToVersion1(t *testing.T) {
	s := openTest(t)
	var v int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version: want %d, got %d", schemaVersion, v)
	}
}

func TestOpen_MigrationsAreIdempotent(t *testing.T) {
	// Per F-9 plan note: re-running schema on a DB with existing data must
	// preserve rows. We seed a provider, "re-open" by calling applyMigrations
	// again on the same DB, and assert the row survives.
	s := openTest(t)
	if err := s.AddProvider(Provider{
		Name: "p1", Kind: "deepseek", OpenAIBaseURL: "https://api.deepseek.com",
		APIKey: "k", IsDefault: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(s.DB()); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	got, err := s.GetProvider("p1")
	if err != nil {
		t.Fatalf("GetProvider after re-migrate: %v", err)
	}
	if got.Name != "p1" || got.Kind != "deepseek" {
		t.Errorf("row mutated: %+v", got)
	}
}

func TestProvider_AddGetRemove(t *testing.T) {
	s := openTest(t)
	p := Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL:    "https://api.deepseek.com",
		AnthropicBaseURL: "https://api.deepseek.com/anthropic",
		APIKey:           "k1",
	}
	if err := s.AddProvider(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != p.Name || got.OpenAIBaseURL != p.OpenAIBaseURL || got.AnthropicBaseURL != p.AnthropicBaseURL || got.APIKey != p.APIKey {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.AnthropicVersion != DefaultAnthropicVersion {
		t.Errorf("AnthropicVersion default: want %q, got %q", DefaultAnthropicVersion, got.AnthropicVersion)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt must be set")
	}

	if err := s.RemoveProvider("deepseek"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProvider("deepseek"); err != ErrNotFound {
		t.Errorf("after remove: want ErrNotFound, got %v", err)
	}
}

func TestProvider_CheckConstraint_RequireAtLeastOneBaseURL(t *testing.T) {
	s := openTest(t)
	err := s.AddProvider(Provider{Name: "bad", Kind: "deepseek", APIKey: "k"})
	if err == nil {
		t.Fatalf("expected CHECK violation when both base URLs nil")
	}
}

func TestProvider_AddDuplicateName_Errors(t *testing.T) {
	s := openTest(t)
	p := Provider{Name: "x", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"}
	if err := s.AddProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProvider(p); err != ErrDuplicate {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestProvider_SetDefaultProvider_UnsetsPrevious(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "a", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k", IsDefault: true})
	_ = s.AddProvider(Provider{Name: "b", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	if err := s.SetDefaultProvider("b"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetProvider("a")
	b, _ := s.GetProvider("b")
	if a.IsDefault {
		t.Errorf("a should no longer be default")
	}
	if !b.IsDefault {
		t.Errorf("b should be default")
	}
}

func TestProvider_ListProviders(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "z", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.AddProvider(Provider{Name: "a", Kind: "glm", OpenAIBaseURL: "u", APIKey: "k"})
	list, err := s.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 providers, got %d", len(list))
	}
	// list ordered by name asc
	if list[0].Name != "a" || list[1].Name != "z" {
		t.Errorf("not alphabetical: %v %v", list[0].Name, list[1].Name)
	}
}

func TestAlias_SetGetList_CascadeOnProviderRemove(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	if err := s.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlias("smart", "deepseek", "deepseek-v4-pro", nil, nil); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 aliases, got %d", len(list))
	}

	// Cascade: remove provider → aliases vanish
	if err := s.RemoveProvider("deepseek"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListAliases()
	if len(list) != 0 {
		t.Errorf("aliases not cascaded: %v", list)
	}
}

func TestAlias_Upsert(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	_ = s.SetAlias("fast", "p", "model-1", nil, nil)
	_ = s.SetAlias("fast", "p", "model-2", nil, nil) // overwrite
	a, _ := s.ResolveAlias("fast")
	if a.UpstreamModel != "model-2" {
		t.Errorf("upsert did not overwrite: %+v", a)
	}
}

func TestAlias_PointsToMissingProvider_Errors(t *testing.T) {
	s := openTest(t)
	err := s.SetAlias("fast", "ghost", "model", nil, nil)
	if err == nil {
		t.Fatalf("expected FK error when provider does not exist")
	}
}

func TestLog_InsertAndTail(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	rec := RequestLog{
		Ts:               time.Now(),
		ClientModel:      "smart",
		ResolvedModel:    "deepseek-v4-pro",
		ProviderName:     "deepseek",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		LatencyMs:        1500,
		Status:           "ok",
		PromptExcerpt:    "hi",
	}
	id, err := s.LogRequest(rec)
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("want id > 0, got %d", id)
	}

	tail, err := s.TailLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 {
		t.Fatalf("want 1 row, got %d", len(tail))
	}
	if tail[0].ProviderName != "deepseek" || tail[0].PromptTokens != 10 {
		t.Errorf("tail row mismatch: %+v", tail[0])
	}

	got, err := s.GetRequestLog(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Status != "ok" {
		t.Errorf("get-by-id mismatch: %+v", got)
	}
}

func TestLog_TailOrderByTsDesc(t *testing.T) {
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	now := time.Now()
	for i := 0; i < 3; i++ {
		_, _ = s.LogRequest(RequestLog{
			Ts: now.Add(time.Duration(i) * time.Second), ClientModel: "x", ResolvedModel: "y",
			ProviderName: "p", Status: "ok",
		})
	}
	tail, _ := s.TailLogs(10)
	if len(tail) != 3 {
		t.Fatalf("want 3, got %d", len(tail))
	}
	// Newest first
	if !tail[0].Ts.After(tail[2].Ts) {
		t.Errorf("TailLogs not ordered desc: %v %v", tail[0].Ts, tail[2].Ts)
	}
}

// --- team-scoped alias tests (T18) ---

func TestTeamAlias_SetListRemove(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme", "Acme")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	if err := s.SetAliasForTeam("fast", team.ID, "p", "deepseek-v4-flash", nil, nil); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListAliasesForTeam(team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Alias != "fast" || list[0].UpstreamModel != "deepseek-v4-flash" {
		t.Errorf("ListAliasesForTeam: %+v", list)
	}

	// ListAliases (global) must NOT return team-scoped aliases
	globals, _ := s.ListAliases()
	if len(globals) != 0 {
		t.Errorf("ListAliases returned team-scoped alias: %v", globals)
	}

	if err := s.RemoveAliasForTeam("fast", team.ID); err != nil {
		t.Fatal(err)
	}
	list2, _ := s.ListAliasesForTeam(team.ID)
	if len(list2) != 0 {
		t.Errorf("alias not removed: %v", list2)
	}
}

func TestTeamAlias_Upsert(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme2", "Acme2")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})

	_ = s.SetAliasForTeam("smart", team.ID, "p", "model-v1", nil, nil)
	_ = s.SetAliasForTeam("smart", team.ID, "p", "model-v2", nil, nil)

	a, err := s.ResolveAliasForTeam("smart", team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.UpstreamModel != "model-v2" {
		t.Errorf("upsert did not overwrite: %s", a.UpstreamModel)
	}
}

func TestTeamAlias_TeamScopeOverridesGlobal(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme3", "Acme3")
	_ = s.AddProvider(Provider{Name: "p1", Kind: "deepseek", OpenAIBaseURL: "u1", APIKey: "k"})
	_ = s.AddProvider(Provider{Name: "p2", Kind: "glm", OpenAIBaseURL: "u2", APIKey: "k"})

	_ = s.SetAlias("fast", "p1", "global-model", nil, nil)
	_ = s.SetAliasForTeam("fast", team.ID, "p2", "team-model", nil, nil)

	a, err := s.ResolveAliasForTeam("fast", team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProviderName != "p2" || a.UpstreamModel != "team-model" {
		t.Errorf("team alias not selected: %+v", a)
	}

	// Empty teamID must still pick global alias
	ag, err := s.ResolveAliasForTeam("fast", "")
	if err != nil {
		t.Fatal(err)
	}
	if ag.ProviderName != "p1" || ag.UpstreamModel != "global-model" {
		t.Errorf("empty teamID should pick global alias: %+v", ag)
	}
}

func TestTeamAlias_FallsBackToGlobal(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme4", "Acme4")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.SetAlias("fast", "p", "global-model", nil, nil)
	// Team has no "fast" alias of its own

	a, err := s.ResolveAliasForTeam("fast", team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.UpstreamModel != "global-model" {
		t.Errorf("did not fall back to global alias: %+v", a)
	}
}

func TestTeamAlias_NotFound(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme5", "Acme5")

	_, err := s.ResolveAliasForTeam("nonexistent", team.ID)
	if err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestTeamAlias_CascadeOnTeamRemove(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme6", "Acme6")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.SetAliasForTeam("fast", team.ID, "p", "model", nil, nil)

	// No api_keys for this team so ON DELETE RESTRICT doesn't block us
	if err := s.RemoveTeam(team.ID); err != nil {
		t.Fatalf("RemoveTeam: %v", err)
	}
	list, _ := s.ListAliasesForTeam(team.ID)
	if len(list) != 0 {
		t.Errorf("team alias not cascaded on team delete: %v", list)
	}
}

func TestResolveAlias_GlobalOnly(t *testing.T) {
	// ResolveAlias must not return team-scoped aliases even when alias name matches
	s := openTest(t)
	team, _ := s.AddTeam("acme7", "Acme7")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.SetAliasForTeam("fast", team.ID, "p", "team-model", nil, nil)
	// No global alias for "fast"

	_, err := s.ResolveAlias("fast")
	if err != ErrNotFound {
		t.Errorf("ResolveAlias should ignore team-scoped aliases, got %v", err)
	}
}

func TestRemoveAlias_GlobalOnly(t *testing.T) {
	// RemoveAlias must not delete team-scoped aliases
	s := openTest(t)
	team, _ := s.AddTeam("acme8", "Acme8")
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	_ = s.SetAliasForTeam("fast", team.ID, "p", "team-model", nil, nil)

	err := s.RemoveAlias("fast")
	if err != ErrNotFound {
		t.Errorf("RemoveAlias should not remove team-scoped alias, got %v", err)
	}
	// Team alias must still exist
	list, _ := s.ListAliasesForTeam(team.ID)
	if len(list) != 1 {
		t.Errorf("team alias should survive RemoveAlias: %v", list)
	}
}

func TestOpen_FileBacked_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	{
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
		s.Close()
	}
	{
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		got, err := s.GetProvider("p")
		if err != nil {
			t.Fatalf("re-open lost row: %v", err)
		}
		if got.Name != "p" {
			t.Errorf("re-open mismatch: %+v", got)
		}
	}
}
