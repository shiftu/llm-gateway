package store

import "testing"

func TestFallbackPolicy_SetListRemove(t *testing.T) {
	s := openTest(t)

	id, err := s.SetFallbackPolicy(FallbackPolicy{
		Trigger:       "http_5xx",
		Action:        "next_best",
		MaxChainDepth: 3,
	})
	if err != nil {
		t.Fatalf("SetFallbackPolicy: %v", err)
	}
	if id <= 0 {
		t.Fatalf("want id > 0, got %d", id)
	}

	list, err := s.ListFallbackPolicies("")
	if err != nil {
		t.Fatalf("ListFallbackPolicies: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 policy, got %d", len(list))
	}
	p := list[0]
	if p.Trigger != "http_5xx" || p.Action != "next_best" || p.MaxChainDepth != 3 {
		t.Errorf("round-trip mismatch: %+v", p)
	}

	if err := s.RemoveFallbackPolicy(id); err != nil {
		t.Fatalf("RemoveFallbackPolicy: %v", err)
	}
	list, _ = s.ListFallbackPolicies("")
	if len(list) != 0 {
		t.Errorf("expected empty after remove, got %d", len(list))
	}
}

func TestFallbackPolicy_TeamOverridesGlobal(t *testing.T) {
	s := openTest(t)
	team, _ := s.AddTeam("acme", "Acme")

	// Global policy
	_, _ = s.SetFallbackPolicy(FallbackPolicy{
		Trigger: "http_5xx", Action: "next_best", MaxChainDepth: 2,
	})
	// Team-scoped policy
	_, _ = s.SetFallbackPolicy(FallbackPolicy{
		TeamID: team.ID, Trigger: "http_5xx", Action: "specific_provider",
		TargetProvider: "backup", MaxChainDepth: 1,
	})

	got, ok, err := s.GetEffectiveFallbackPolicy("http_5xx", team.ID)
	if err != nil {
		t.Fatalf("GetEffectiveFallbackPolicy: %v", err)
	}
	if !ok {
		t.Fatal("expected policy found, got not-found")
	}
	if got.Action != "specific_provider" || got.TargetProvider != "backup" {
		t.Errorf("expected team policy to win, got %+v", got)
	}
}

func TestFallbackPolicy_GlobalFallback(t *testing.T) {
	s := openTest(t)

	_, _ = s.SetFallbackPolicy(FallbackPolicy{
		Trigger: "http_429", Action: "next_best", MaxChainDepth: 3,
	})

	got, ok, err := s.GetEffectiveFallbackPolicy("http_429", "some-team-id")
	if err != nil {
		t.Fatalf("GetEffectiveFallbackPolicy: %v", err)
	}
	if !ok {
		t.Fatal("expected global policy found when no team policy exists")
	}
	if got.Action != "next_best" {
		t.Errorf("got %+v", got)
	}
}

func TestFallbackPolicy_NotFound(t *testing.T) {
	s := openTest(t)

	_, ok, err := s.GetEffectiveFallbackPolicy("http_5xx", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected not-found for empty store")
	}
}

func TestFallbackPolicy_Upsert(t *testing.T) {
	s := openTest(t)

	_, _ = s.SetFallbackPolicy(FallbackPolicy{
		Trigger: "http_5xx", Action: "next_best", MaxChainDepth: 2,
	})
	_, _ = s.SetFallbackPolicy(FallbackPolicy{
		Trigger: "http_5xx", Action: "specific_provider", TargetProvider: "p2", MaxChainDepth: 1,
	})

	list, _ := s.ListFallbackPolicies("")
	if len(list) != 1 {
		t.Fatalf("upsert should keep 1 row, got %d", len(list))
	}
	if list[0].Action != "specific_provider" {
		t.Errorf("upsert didn't overwrite: %+v", list[0])
	}
}
