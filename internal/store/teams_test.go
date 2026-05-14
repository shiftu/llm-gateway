package store

import (
	"errors"
	"strings"
	"testing"
)

// teams_test.go covers T4 rework — Go API for the teams table. Teams are the
// tenancy boundary that api_keys + (optionally) providers + model_aliases hang
// off; per Plan R3 FINAL there are no users in v0.1, just team-scoped keys
// with a `created_for_label` describing each issuance.

func TestTeam_AddGet(t *testing.T) {
	s := openTest(t)
	tm, err := s.AddTeam("acme", "Acme Corp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tm.ID, "tm_") {
		t.Errorf("team id must start with tm_, got %q", tm.ID)
	}
	if tm.Slug != "acme" || tm.Name != "Acme Corp" {
		t.Errorf("round-trip mismatch: %+v", tm)
	}
	if tm.CreatedAt.IsZero() {
		t.Errorf("CreatedAt must be set")
	}

	got, err := s.GetTeam(tm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "acme" {
		t.Errorf("GetTeam: %+v", got)
	}
}

func TestTeam_GetBySlug(t *testing.T) {
	s := openTest(t)
	_, _ = s.AddTeam("acme", "Acme Corp")
	got, err := s.GetTeamBySlug("acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Corp" {
		t.Errorf("slug lookup mismatch: %+v", got)
	}
	if _, err := s.GetTeamBySlug("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestTeam_AddDuplicateSlug_Errors(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddTeam("acme", "Acme A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTeam("acme", "Acme B"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("want ErrDuplicate, got %v", err)
	}
}

func TestTeam_AddInvalidSlug_Errors(t *testing.T) {
	s := openTest(t)
	for _, bad := range []string{"", "with spaces", "UPPER", "x", strings.Repeat("a", 65)} {
		if _, err := s.AddTeam(bad, "x"); err == nil {
			t.Errorf("slug %q must be rejected", bad)
		}
	}
}

func TestTeam_List(t *testing.T) {
	s := openTest(t)
	_, _ = s.AddTeam("zeta", "Z")
	_, _ = s.AddTeam("alpha", "A")
	list, err := s.ListTeams()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 teams, got %d", len(list))
	}
	// Ordered by slug ASC, matching provider list convention.
	if list[0].Slug != "alpha" || list[1].Slug != "zeta" {
		t.Errorf("not alphabetical: %v %v", list[0].Slug, list[1].Slug)
	}
}

func TestTeam_Remove(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	if err := s.RemoveTeam(tm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTeam(tm.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after remove, got %v", err)
	}
}

func TestTeam_Remove_BlockedByAPIKey_RESTRICT(t *testing.T) {
	// Per ENG-F11: api_keys.team_id is FK ON DELETE RESTRICT so the audit
	// chain (request_logs.api_key_id) cannot be broken by deleting a team
	// that still has keys. Removal must fail with a clear error.
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	_, _, err := s.IssueAPIKey(tm.ID, "mcp_admin", "ci-bot")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTeam(tm.ID); err == nil {
		t.Errorf("RemoveTeam must fail while api_keys exist (FK RESTRICT)")
	}
}

func TestTeam_RemoveMissing_ErrNotFound(t *testing.T) {
	s := openTest(t)
	if err := s.RemoveTeam("tm_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
