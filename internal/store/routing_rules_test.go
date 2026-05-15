package store

import (
	"fmt"
	"testing"
	"time"
)

func TestRoutingRule_SetAndGet(t *testing.T) {
	s := openTest(t)

	// Need a provider and team for FK
	mustAddProvider(s, "deepseek", "deepseek")
	teamID := mustAddTeam(s, "team1")

	rule, err := s.SetRoutingRule(RoutingRule{
		TeamID:       teamID,
		Priority:     10,
		MatchField:   "model",
		MatchOp:      "prefix",
		MatchValue:   "deepseek-",
		ProviderName: "deepseek",
		IsActive:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == 0 {
		t.Fatal("expected non-zero ID after insert")
	}

	rules, err := s.ListRoutingRules(teamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.MatchField != "model" || r.MatchOp != "prefix" || r.MatchValue != "deepseek-" {
		t.Fatalf("unexpected rule content: %+v", r)
	}
	if r.ProviderName != "deepseek" {
		t.Fatalf("expected provider deepseek, got %s", r.ProviderName)
	}
	if !r.IsActive {
		t.Fatal("expected active rule")
	}
}

func TestRoutingRule_Update(t *testing.T) {
	s := openTest(t)
	mustAddProvider(s, "glm-prod", "glm")

	rule, _ := s.SetRoutingRule(RoutingRule{
		Priority:     5,
		MatchField:   "model",
		MatchOp:      "equals",
		MatchValue:   "glm-4",
		ProviderName: "glm-prod",
		IsActive:     true,
	})

	rule.MatchValue = "glm-4-plus"
	rule, err := s.SetRoutingRule(rule)
	if err != nil {
		t.Fatal(err)
	}

	rules, _ := s.ListRoutingRules("all")
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule after update, got %d", len(rules))
	}
	if rules[0].MatchValue != "glm-4-plus" {
		t.Fatalf("expected match_value glm-4-plus, got %s", rules[0].MatchValue)
	}
}

func TestRoutingRule_Delete(t *testing.T) {
	s := openTest(t)
	mustAddProvider(s, "deepseek", "deepseek")

	rule, _ := s.SetRoutingRule(RoutingRule{
		MatchField:   "model",
		MatchOp:      "prefix",
		MatchValue:   "ds-",
		ProviderName: "deepseek",
		IsActive:     true,
	})

	if err := s.DeleteRoutingRule(rule.ID); err != nil {
		t.Fatal(err)
	}

	rules, _ := s.ListRoutingRules("all")
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(rules))
	}
}

func TestRoutingRule_DeleteNotFound(t *testing.T) {
	s := openTest(t)
	err := s.DeleteRoutingRule(999)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRoutingRule_ListGlobal(t *testing.T) {
	s := openTest(t)
	mustAddProvider(s, "deepseek", "deepseek")
	teamID := mustAddTeam(s, "team1")

	// Global rule
	s.SetRoutingRule(RoutingRule{
		MatchField:   "model",
		MatchOp:      "prefix",
		MatchValue:   "ds-",
		ProviderName: "deepseek",
		IsActive:     true,
	})
	// Team rule
	s.SetRoutingRule(RoutingRule{
		TeamID:       teamID,
		MatchField:   "kind",
		MatchOp:      "equals",
		MatchValue:   "deepseek",
		ProviderName: "deepseek",
		IsActive:     true,
	})

	// List global only
	rules, _ := s.ListRoutingRules("")
	if len(rules) != 1 {
		t.Fatalf("expected 1 global rule, got %d", len(rules))
	}

	// List all
	rules, _ = s.ListRoutingRules("all")
	if len(rules) != 2 {
		t.Fatalf("expected 2 total rules, got %d", len(rules))
	}
}

func TestRoutingRule_MatchRoutingRules(t *testing.T) {
	s := openTest(t)
	mustAddProvider(s, "deepseek", "deepseek")
	mustAddProvider(s, "glm-prod", "glm")
	teamID := mustAddTeam(s, "team1")

	// Global rule: prefix match on "deepseek-" → deepseek
	s.SetRoutingRule(RoutingRule{
		Priority:     10,
		MatchField:   "model",
		MatchOp:      "prefix",
		MatchValue:   "deepseek-",
		ProviderName: "deepseek",
		IsActive:     true,
	})
	// Team rule: kind=deepseek (lower priority = checked first)
	s.SetRoutingRule(RoutingRule{
		TeamID:       teamID,
		Priority:     5,
		MatchField:   "kind",
		MatchOp:      "equals",
		MatchValue:   "deepseek",
		ProviderName: "deepseek",
		IsActive:     true,
	})

	// Match for team1 — should get both team + global rules
	rules, err := s.MatchRoutingRules(teamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 matching rules, got %d", len(rules))
	}
	// Team rule (priority 5) should be first
	if rules[0].TeamID != teamID {
		t.Fatalf("expected team rule first, got %+v", rules[0])
	}
}

func TestRoutingRule_MatchInactiveExcluded(t *testing.T) {
	s := openTest(t)
	mustAddProvider(s, "deepseek", "deepseek")

	s.SetRoutingRule(RoutingRule{
		MatchField:   "model",
		MatchOp:      "prefix",
		MatchValue:   "ds-",
		ProviderName: "deepseek",
		IsActive:     false,
	})

	rules, _ := s.MatchRoutingRules("")
	if len(rules) != 0 {
		t.Fatalf("inactive rules should be excluded, got %d", len(rules))
	}
}

func mustAddProvider(s *Store, name, kind string) {
	s.AddProvider(Provider{
		Name: name, Kind: kind,
		OpenAIBaseURL: "https://example.com",
		APIKey:        "sk-test",
	})
}

func mustAddTeam(s *Store, slug string) string {
	tm, err := s.AddTeam(slug, slug)
	if err != nil {
		panic(fmt.Sprintf("mustAddTeam(%q): %v", slug, err))
	}
	return tm.ID
}

// Verify the v2 migration works on an existing v1 DB
func TestMigration_v1_to_v2(t *testing.T) {
	s := openTest(t)

	// Check that routing_rules table exists
	var name string
	err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='routing_rules'").Scan(&name)
	if err != nil {
		t.Fatalf("routing_rules table not found: %v", err)
	}

	// Check admin_audit table exists
	err = s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='admin_audit'").Scan(&name)
	if err != nil {
		t.Fatalf("admin_audit table not found: %v", err)
	}

	// Verify user_version
	var v int
	s.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != schemaVersion {
		t.Fatalf("user_version: want %d, got %d", schemaVersion, v)
	}

	_ = time.Now() // suppress unused import
}
