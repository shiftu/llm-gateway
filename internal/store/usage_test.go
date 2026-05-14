package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// usage_test.go covers T4 rework — Go API for usage_counters + model_costs.
// usage_counters uses an atomic UPSERT-with-increment pattern so the quota
// middleware (T10) can `reserve` a request slot under concurrent inbound
// load without races. model_costs supports time-versioned pricing: getter
// picks the most-recent row with effective_from <= asOf.

func TestUsage_Reserve_FirstCallSeedsRow(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	day := dayUTC(time.Now())

	c, err := s.ReserveQuota(ak.ID, day, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.RequestCount != 1 {
		t.Errorf("first reserve should set request_count=1, got %d", c.RequestCount)
	}
	if c.APIKeyID != ak.ID || c.DayUTC != day {
		t.Errorf("counter key mismatch: %+v", c)
	}
}

func TestUsage_Reserve_IncrementsExisting(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	day := dayUTC(time.Now())

	_, _ = s.ReserveQuota(ak.ID, day, 1)
	_, _ = s.ReserveQuota(ak.ID, day, 1)
	c, err := s.ReserveQuota(ak.ID, day, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.RequestCount != 3 {
		t.Errorf("three reserves should yield 3, got %d", c.RequestCount)
	}
}

func TestUsage_Reserve_Concurrent(t *testing.T) {
	// Atomic UPDATE-RETURNING (or UPSERT-on-conflict-DO-UPDATE-RETURNING)
	// is the contract here: even under N concurrent reservers we must end
	// up with request_count == N. Verifies no lost updates.
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	day := dayUTC(time.Now())

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := s.ReserveQuota(ak.ID, day, 1); err != nil {
				t.Errorf("concurrent reserve: %v", err)
			}
		}()
	}
	wg.Wait()

	c, _ := s.GetUsageCounter(ak.ID, day)
	if c.RequestCount != N {
		t.Errorf("lost updates: want %d, got %d", N, c.RequestCount)
	}
}

func TestUsage_CommitUsage_AddsTokensAndCost(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	day := dayUTC(time.Now())

	_, _ = s.ReserveQuota(ak.ID, day, 1)
	if err := s.CommitUsage(ak.ID, day, 100, 50, 20, 75_000); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitUsage(ak.ID, day, 200, 100, 0, 125_000); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetUsageCounter(ak.ID, day)
	if c.InputTokens != 300 || c.OutputTokens != 150 {
		t.Errorf("token deltas: %+v", c)
	}
	if c.ReasoningTokens != 20 {
		t.Errorf("reasoning tokens: %+v", c)
	}
	if c.CostUSDMicros != 200_000 {
		t.Errorf("cost: want 200_000 µ$, got %d", c.CostUSDMicros)
	}
}

func TestUsage_CommitWithoutReserve_SeedsRow(t *testing.T) {
	// Defensive: commit should not panic if reserve was skipped (e.g., test
	// path or upstream-only billing reconciliation later).
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	day := dayUTC(time.Now())

	if err := s.CommitUsage(ak.ID, day, 10, 5, 0, 1000); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetUsageCounter(ak.ID, day)
	if c.InputTokens != 10 || c.OutputTokens != 5 || c.CostUSDMicros != 1000 {
		t.Errorf("commit-without-reserve mismatch: %+v", c)
	}
}

func TestUsage_GetMissing_ErrNotFound(t *testing.T) {
	s := openTest(t)
	tm, _ := s.AddTeam("acme", "A")
	ak, _, _ := s.IssueAPIKey(tm.ID, "inbound", "")
	if _, err := s.GetUsageCounter(ak.ID, dayUTC(time.Now())); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestModelCost_SetGet(t *testing.T) {
	s := openTest(t)
	c := ModelCost{
		Provider: "deepseek", Model: "deepseek-v4-pro",
		USDPerInput1k: 0.27, USDPerOutput1k: 1.10,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.SetModelCost(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelCost("deepseek", "deepseek-v4-pro", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.USDPerInput1k != 0.27 || got.USDPerOutput1k != 1.10 {
		t.Errorf("cost round-trip: %+v", got)
	}
}

func TestModelCost_GetPicksLatestEffectiveBeforeAsOf(t *testing.T) {
	s := openTest(t)
	_ = s.SetModelCost(ModelCost{
		Provider: "deepseek", Model: "x", USDPerInput1k: 1.0, USDPerOutput1k: 1.0,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	_ = s.SetModelCost(ModelCost{
		Provider: "deepseek", Model: "x", USDPerInput1k: 2.0, USDPerOutput1k: 2.0,
		EffectiveFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	})
	_ = s.SetModelCost(ModelCost{
		Provider: "deepseek", Model: "x", USDPerInput1k: 3.0, USDPerOutput1k: 3.0,
		EffectiveFrom: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	})

	// As-of in May should pick the April row (the most recent <= asOf).
	got, _ := s.GetModelCost("deepseek", "x", time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if got.USDPerInput1k != 2.0 {
		t.Errorf("as-of May should return April pricing (2.0), got %v", got.USDPerInput1k)
	}
	// As-of in August should pick the July row.
	got, _ = s.GetModelCost("deepseek", "x", time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if got.USDPerInput1k != 3.0 {
		t.Errorf("as-of Aug should return July pricing (3.0), got %v", got.USDPerInput1k)
	}
}

func TestModelCost_GetBeforeAnyEffectiveDate_ErrNotFound(t *testing.T) {
	s := openTest(t)
	_ = s.SetModelCost(ModelCost{
		Provider: "deepseek", Model: "x", USDPerInput1k: 1.0, USDPerOutput1k: 1.0,
		EffectiveFrom: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})
	if _, err := s.GetModelCost("deepseek", "x", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrNotFound) {
		t.Errorf("as-of before all rows should ErrNotFound, got %v", err)
	}
}

func TestModelCost_List(t *testing.T) {
	s := openTest(t)
	_ = s.SetModelCost(ModelCost{
		Provider: "deepseek", Model: "x", USDPerInput1k: 1.0, USDPerOutput1k: 1.0,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	_ = s.SetModelCost(ModelCost{
		Provider: "glm", Model: "y", USDPerInput1k: 0.5, USDPerOutput1k: 1.5,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	list, err := s.ListModelCosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("want 2, got %d", len(list))
	}
}

// dayUTC returns the integer ms-epoch start-of-day for t in UTC, matching
// the convention used by the quota middleware and aggregations.
func dayUTC(t time.Time) int64 {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}
