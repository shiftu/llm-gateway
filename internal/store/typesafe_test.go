package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/panda/llm-gateway/internal/encrypt"
)

func TestTypeSafeProviderRoundTrip(t *testing.T) {
	s := openTest(t)
	mk, err := encrypt.NewFromBytes(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.SetEncryptor(&testEncryptor{mk: mk})
	p := Provider{Name: "typesafe", Kind: "typesafe", TypeSafeBaseURL: "https://api.typesafe.ai/v1", APIKey: "test-secret", IsDefault: true}
	if err := s.AddProvider(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(p.Name)
	if err != nil || got.TypeSafeBaseURL != p.TypeSafeBaseURL || got.APIKey != p.APIKey || got.OpenAIBaseURL != "" {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	list, err := s.ListProviders()
	if err != nil || len(list) != 1 || list[0].TypeSafeBaseURL != p.TypeSafeBaseURL {
		t.Fatalf("list: %+v, %v", list, err)
	}
	def, err := s.GetDefaultProvider()
	if err != nil || def.TypeSafeBaseURL != p.TypeSafeBaseURL {
		t.Fatalf("default: %+v, %v", def, err)
	}
	var key string
	if err := s.db.QueryRow("SELECT api_key FROM providers WHERE name = ?", p.Name).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if !encrypt.IsEncrypted(key) {
		t.Fatal("provider key is not encrypted")
	}
}

func TestTypeSafeMigrationPreservesV12Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v12.db")
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Build the actual historical schema, including its original URL CHECK.
	for _, migration := range migrations[:12] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		"PRAGMA user_version = 12",
		"INSERT INTO teams (id, slug, name, created_at) VALUES ('team', 'team', 'Team', 123)",
		"INSERT INTO providers (name,kind,openai_base_url,api_key,is_default,team_id,fallback_eligible,created_at) VALUES ('chat','openai','https://example.com/v1','encrypted-or-plain',1,'team',1,123)",
		"INSERT INTO model_aliases (alias,provider_name,upstream_model,created_at) VALUES ('global','chat','model',123)",
		"INSERT INTO model_aliases (alias,team_id,provider_name,upstream_model,created_at) VALUES ('scoped','team','chat','model',123)",
		"INSERT INTO routing_rules (match_value,provider_name,created_at) VALUES ('model','chat',123)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.GetDefaultProvider()
	if err != nil || p.Name != "chat" || p.APIKey != "encrypted-or-plain" || p.CreatedAt.UnixMilli() != 123 || p.TypeSafeBaseURL != "" {
		t.Fatalf("provider changed: %+v, %v", p, err)
	}
	for table, want := range map[string]int{"model_aliases": 2, "routing_rules": 1} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: got %d, want %d, %v", table, n, want, err)
		}
	}
	var team string
	var eligible, foreignKeys int
	if err := s.db.QueryRow("SELECT team_id, fallback_eligible FROM providers WHERE name='chat'").Scan(&team, &eligible); err != nil || team != "team" || eligible != 1 {
		t.Fatalf("provider metadata: %s %d %v", team, eligible, err)
	}
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("FK enforcement: %d %v", foreignKeys, err)
	}
	if err := applyMigrations(s.db); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProvider(Provider{Name: "typesafe", Kind: "typesafe", TypeSafeBaseURL: "https://api.typesafe.ai/v1", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProvider(Provider{Name: "bad", Kind: "custom", APIKey: "k"}); err == nil {
		t.Fatal("URL constraint lost")
	}
	if err := s.RemoveProvider("chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAlias("global"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cascade lost: %v", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM routing_rules").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rules cascade: %d %v", n, err)
	}
}
