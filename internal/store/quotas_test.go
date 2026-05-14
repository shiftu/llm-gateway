package store

import (
	"errors"
	"testing"
)

// quotas_test.go covers T4 rework — Go API for the quotas table. Quotas are
// (scope_kind, scope_id, window) primary key with optional max_requests /
// max_tokens / max_usd_micros. Used by the quota middleware (T10) to gate
// inbound requests; the schema enforces enum values at the DB layer.

func TestQuota_SetGet(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")

	q := Quota{
		ScopeKind: "team", ScopeID: tm.ID, Window: "day",
		MaxRequests: int64Ptr(1000), MaxUSDMicros: int64Ptr(5_000_000),
	}
	if err := s.SetQuota(q); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetQuota("team", tm.ID, "day")
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxRequests == nil || *got.MaxRequests != 1000 {
		t.Errorf("max_requests round-trip: %+v", got)
	}
	if got.MaxUSDMicros == nil || *got.MaxUSDMicros != 5_000_000 {
		t.Errorf("max_usd_micros round-trip: %+v", got)
	}
	if got.MaxTokens != nil {
		t.Errorf("unset max_tokens must be nil, got %v", *got.MaxTokens)
	}
}

func TestQuota_Upsert(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "month", MaxRequests: int64Ptr(100)})
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "month", MaxRequests: int64Ptr(200)})
	got, _ := s.GetQuota("team", tm.ID, "month")
	if *got.MaxRequests != 200 {
		t.Errorf("upsert did not overwrite: %v", *got.MaxRequests)
	}
}

func TestQuota_GetMissing_ErrNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetQuota("team", "tm_ghost", "day"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestQuota_SetRejectsBadScopeKind(t *testing.T) {
	s := openTest(t)
	err := s.SetQuota(Quota{ScopeKind: "user", ScopeID: "x", Window: "day", MaxRequests: int64Ptr(1)})
	if err == nil {
		t.Errorf("scope_kind 'user' must violate CHECK")
	}
}

func TestQuota_SetRejectsBadWindow(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	err := s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "hour", MaxRequests: int64Ptr(1)})
	if err == nil {
		t.Errorf("window 'hour' must violate CHECK")
	}
}

func TestQuota_ListForScope(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "day", MaxRequests: int64Ptr(100)})
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "month", MaxRequests: int64Ptr(3000)})
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: "tm_other", Window: "day", MaxRequests: int64Ptr(50)})

	list, err := s.ListQuotasForScope("team", tm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 quotas for tm.ID, got %d", len(list))
	}
}

func TestQuota_Delete(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	_ = s.SetQuota(Quota{ScopeKind: "team", ScopeID: tm.ID, Window: "day", MaxRequests: int64Ptr(1)})
	if err := s.DeleteQuota("team", tm.ID, "day"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetQuota("team", tm.ID, "day"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
	if err := s.DeleteQuota("team", tm.ID, "day"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete-missing should ErrNotFound, got %v", err)
	}
}

func int64Ptr(v int64) *int64 { return &v }
