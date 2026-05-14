package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// api_keys_test.go covers T4 rework — Go API for api_keys + scope CHECK
// enforcement at the Go layer. Per ENG-F5 the 16-char prefix must be UNIQUE
// (verified at the schema test layer) and the issuer must regenerate on
// collision — exercised here indirectly by issuing many keys back-to-back
// and asserting all succeed with distinct prefixes. The plaintext token is
// returned ONCE at issuance; subsequent lookups operate on the bcrypt hash.

func TestAPIKey_IssueReturnsPlaintextOnce(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")

	ak, plaintext, err := s.IssueAPIKey(tm.ID, "mcp_admin", "ci-bot")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" {
		t.Fatal("plaintext must be returned at issuance")
	}
	if !strings.HasPrefix(plaintext, "lgw_") {
		t.Errorf("plaintext should start with lgw_, got %q", plaintext)
	}
	if !strings.HasPrefix(ak.Prefix, "lgw_") || len(ak.Prefix) != 20 {
		// Per ENG-F5: 16-char body + "lgw_" prefix = 20 chars total
		t.Errorf("prefix shape wrong: %q (len=%d)", ak.Prefix, len(ak.Prefix))
	}
	if !strings.HasPrefix(plaintext, ak.Prefix) {
		t.Errorf("plaintext %q must start with stored prefix %q", plaintext, ak.Prefix)
	}
	if ak.Hash == "" || ak.Hash == plaintext {
		t.Errorf("hash must be set and != plaintext (bcrypt)")
	}
	if ak.Scope != "mcp_admin" {
		t.Errorf("scope round-trip: %q", ak.Scope)
	}
	if ak.CreatedForLabel != "ci-bot" {
		t.Errorf("label round-trip: %q", ak.CreatedForLabel)
	}
	if ak.TeamID != tm.ID {
		t.Errorf("team_id round-trip: %q vs %q", ak.TeamID, tm.ID)
	}
}

func TestAPIKey_IssueRejectsBadScope(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	if _, _, err := s.IssueAPIKey(tm.ID, "root", "x"); err == nil {
		t.Errorf("scope 'root' must be rejected (not in CHECK enum)")
	}
}

func TestAPIKey_IssueRejectsMissingTeam(t *testing.T) {
	s := openTest(t)
	if _, _, err := s.IssueAPIKey("tm_ghost", "mcp_admin", "x"); err == nil {
		t.Errorf("issuing key for non-existent team must fail (FK)")
	}
}

func TestAPIKey_Verify_PlaintextMatches(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	want, plaintext, _ := s.IssueAPIKey(tm.ID, "inbound", "")

	got, err := s.VerifyAPIKey(plaintext)
	if err != nil {
		t.Fatalf("VerifyAPIKey should accept valid plaintext: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("VerifyAPIKey returned wrong row: %q vs %q", got.ID, want.ID)
	}
}

func TestAPIKey_Verify_WrongSecret(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	// Same prefix, mangled secret half.
	if _, err := s.VerifyAPIKey(ak.Prefix + "WRONG_SECRET_TAIL_xxxx"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound on bad secret, got %v", err)
	}
}

func TestAPIKey_Verify_RevokedRejected(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, plaintext, _ := s.IssueAPIKey(tm.ID, "inbound", "")

	if err := s.RevokeAPIKey(ak.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAPIKey(plaintext); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked key must not verify, got %v", err)
	}
}

func TestAPIKey_LookupByPrefix(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	want, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")

	got, err := s.LookupAPIKeyByPrefix(want.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID {
		t.Errorf("lookup-by-prefix mismatch: %q vs %q", got.ID, want.ID)
	}
	if _, err := s.LookupAPIKeyByPrefix("lgw_missing00000xxxx"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestAPIKey_ListByTeam(t *testing.T) {
	s := openTest(t)
	a, _ := s.AddTeam("team-a", "A")
	b, _ := s.AddTeam("team-b", "B")
	_, _, _ = s.IssueAPIKey(a.ID, "inbound", "k1")
	_, _, _ = s.IssueAPIKey(a.ID, "mcp_admin", "k2")
	_, _, _ = s.IssueAPIKey(b.ID, "inbound", "k3")

	list, err := s.ListAPIKeysByTeam(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 keys for team-a, got %d", len(list))
	}
	for _, k := range list {
		if k.TeamID != a.ID {
			t.Errorf("list contained foreign-team key: %+v", k)
		}
	}
}

func TestAPIKey_PrefixesAreDistinctAcrossMany(t *testing.T) {
	// Smoke-test ENG-F5 collision regeneration: issue 50 keys, assert all
	// prefixes distinct (probability of natural collision ≈ 0 over 16 base32
	// chars, so this also catches a regression where the issuer reuses a
	// random source without seeding properly).
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		ak, _, err := s.IssueAPIKey(tm.ID, "inbound", "")
		if err != nil {
			t.Fatal(err)
		}
		if seen[ak.Prefix] {
			t.Fatalf("duplicate prefix %q at i=%d", ak.Prefix, i)
		}
		seen[ak.Prefix] = true
	}
}

func TestAPIKey_TouchLastUsed(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	if ak.LastUsedAt != nil {
		t.Fatalf("fresh key should have nil last_used_at, got %v", ak.LastUsedAt)
	}

	when := time.Now()
	if err := s.TouchAPIKeyLastUsed(ak.ID, when); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LookupAPIKeyByPrefix(ak.Prefix)
	if got.LastUsedAt == nil {
		t.Fatalf("last_used_at not set")
	}
	if got.LastUsedAt.UnixMilli() != when.UnixMilli() {
		t.Errorf("last_used_at: want %v, got %v", when, *got.LastUsedAt)
	}
}

func TestAPIKey_GetAPIKey(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "mcp_super", "founder")

	got, err := s.GetAPIKey(ak.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != "mcp_super" {
		t.Errorf("get round-trip: %+v", got)
	}
	if _, err := s.GetAPIKey("ak_ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id should ErrNotFound, got %v", err)
	}
}
