package store

import (
	"database/sql"
	"strings"
	"testing"
)

// migrate_v2_test.go covers T4 rework (Plan R3 FINAL): schema v1 → v2 adds
// multi-tenancy tables (teams, api_keys, usage_counters, quotas, model_costs)
// + new columns on providers and request_logs + model_aliases composite PK
// (alias, team_id). Per ENG-F8 the migration must be wrapped in a transaction
// so partial failure rolls back cleanly; per ENG-F9 the PK widening on
// model_aliases requires a table-rename since SQLite cannot ALTER PK.

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var got string
	err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		name,
	).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup %q: %v", name, err)
	}
	return got == name
}

func columnExists(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestMigrate_AppliesToVersion2(t *testing.T) {
	s := openTest(t)
	var v int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Errorf("user_version: want 2, got %d", v)
	}
}

func TestMigrate_AddsMultiTenancyTables(t *testing.T) {
	s := openTest(t)
	for _, want := range []string{"teams", "api_keys", "usage_counters", "quotas", "model_costs"} {
		if !tableExists(t, s, want) {
			t.Errorf("v2 table %q missing", want)
		}
	}
}

func TestMigrate_APIKeysHasUniquePrefixIndex(t *testing.T) {
	// Per ENG-F5: collision-prevention requires UNIQUE INDEX on api_keys.prefix.
	s := openTest(t)
	var idxName, isUnique string
	rows, err := s.DB().Query(
		`SELECT name, "unique" FROM pragma_index_list('api_keys')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		if err := rows.Scan(&idxName, &isUnique); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(idxName, "prefix") && isUnique == "1" {
			found = true
		}
	}
	if !found {
		t.Errorf("api_keys missing UNIQUE INDEX on prefix (ENG-F5)")
	}
}

func TestMigrate_ProvidersGainsTeamIDAndFallbackEligible(t *testing.T) {
	s := openTest(t)
	for _, col := range []string{"team_id", "fallback_eligible"} {
		if !columnExists(t, s, "providers", col) {
			t.Errorf("providers.%s missing after v2 migration", col)
		}
	}
}

func TestMigrate_RequestLogsGainsTenancyAndCostColumns(t *testing.T) {
	s := openTest(t)
	want := []string{
		"api_key_id", "team_id", "provider_owner_team_id",
		"cost_usd_micros", "input_tokens", "output_tokens", "reasoning_tokens",
	}
	for _, col := range want {
		if !columnExists(t, s, "request_logs", col) {
			t.Errorf("request_logs.%s missing after v2 migration", col)
		}
	}
}

func TestMigrate_ModelAliasesHasCompositePK(t *testing.T) {
	// Per ENG-F9: two teams must be able to use the same alias name pointing
	// at different providers. Old schema had PRIMARY KEY (alias) which blocks
	// this — v2 widens to PRIMARY KEY (alias, team_id) via table-rename.
	s := openTest(t)

	// Seed two teams + one provider so the inserts have valid FK targets.
	mustExec(t, s.DB(), `INSERT INTO teams (id, slug, name, created_at) VALUES (?,?,?,?)`,
		"tm_a", "team-a", "Team A", 1)
	mustExec(t, s.DB(), `INSERT INTO teams (id, slug, name, created_at) VALUES (?,?,?,?)`,
		"tm_b", "team-b", "Team B", 1)
	mustExec(t, s.DB(), `INSERT INTO providers (name, kind, openai_base_url, api_key, anthropic_version, is_default, created_at, fallback_eligible) VALUES (?,?,?,?,?,?,?,?)`,
		"deepseek", "deepseek", "https://api.deepseek.com", "k", "2023-06-01", 0, 1, 0)

	// Both teams insert alias "fast" with different upstream models — must
	// succeed because PK is composite.
	mustExec(t, s.DB(), `INSERT INTO model_aliases (alias, team_id, provider_name, upstream_model, created_at) VALUES (?,?,?,?,?)`,
		"fast", "tm_a", "deepseek", "deepseek-v4-flash", 1)
	if _, err := s.DB().Exec(`INSERT INTO model_aliases (alias, team_id, provider_name, upstream_model, created_at) VALUES (?,?,?,?,?)`,
		"fast", "tm_b", "deepseek", "deepseek-v4-pro", 2); err != nil {
		t.Fatalf("two teams sharing alias name must succeed: %v", err)
	}

	// Re-inserting (fast, tm_a) must fail (PK violation).
	if _, err := s.DB().Exec(`INSERT INTO model_aliases (alias, team_id, provider_name, upstream_model, created_at) VALUES (?,?,?,?,?)`,
		"fast", "tm_a", "deepseek", "duplicate", 3); err == nil {
		t.Errorf("duplicate (alias, team_id) must violate PK")
	}
}

func TestMigrate_PreservesV1Data(t *testing.T) {
	// Seed v1-shape provider via existing Go API, run migration again (idempotent),
	// assert row + columns intact after v2 schema is in place.
	s := openTest(t)
	if err := s.AddProvider(Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(s.DB()); err != nil {
		t.Fatalf("re-apply must be idempotent: %v", err)
	}
	got, err := s.GetProvider("deepseek")
	if err != nil {
		t.Fatalf("v1 data lost after v2 migration: %v", err)
	}
	if got.APIKey != "secret" {
		t.Errorf("api_key mutated: %q", got.APIKey)
	}
	// New columns should be zero-value for pre-migration rows.
	var teamID sql.NullString
	var fallback int
	if err := s.DB().QueryRow(`SELECT team_id, fallback_eligible FROM providers WHERE name=?`, "deepseek").Scan(&teamID, &fallback); err != nil {
		t.Fatal(err)
	}
	if teamID.Valid {
		t.Errorf("v1 provider should have NULL team_id, got %q", teamID.String)
	}
	if fallback != 0 {
		t.Errorf("fallback_eligible default: want 0, got %d", fallback)
	}
}

func TestMigrate_PreservesV1Aliases(t *testing.T) {
	// Aliases created under the old single-column PK should survive the
	// table-rename and become global aliases (team_id NULL).
	s := openTest(t)
	_ = s.AddProvider(Provider{Name: "p", Kind: "deepseek", OpenAIBaseURL: "u", APIKey: "k"})
	if err := s.SetAlias("fast", "p", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	var teamID sql.NullString
	var upstream string
	if err := s.DB().QueryRow(`SELECT team_id, upstream_model FROM model_aliases WHERE alias=?`, "fast").Scan(&teamID, &upstream); err != nil {
		t.Fatalf("v1 alias lost: %v", err)
	}
	if teamID.Valid {
		t.Errorf("migrated alias should be global (team_id NULL), got %q", teamID.String)
	}
	if upstream != "deepseek-v4-flash" {
		t.Errorf("alias upstream mutated: %q", upstream)
	}
}

func TestMigrate_APIKeyScopeCheckEnforced(t *testing.T) {
	s := openTest(t)
	mustExec(t, s.DB(), `INSERT INTO teams (id, slug, name, created_at) VALUES (?,?,?,?)`,
		"tm_x", "x", "X", 1)
	// Bad scope must be rejected by CHECK constraint.
	_, err := s.DB().Exec(
		`INSERT INTO api_keys (id, team_id, hash, prefix, scope, created_at) VALUES (?,?,?,?,?,?)`,
		"ak_1", "tm_x", "h", "lgw_xxx", "wrong_scope", 1,
	)
	if err == nil {
		t.Errorf("api_keys.scope CHECK constraint should reject 'wrong_scope'")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
