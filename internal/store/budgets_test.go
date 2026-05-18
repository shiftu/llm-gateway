package store

import (
	"errors"
	"testing"
	"time"
)

// budgets_test.go covers T16: per-team budget limits store layer.

func TestSetBudget_RoundTrip(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("acme", "Acme Corp")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	b := Budget{
		TeamID:           team.ID,
		Period:           "month",
		USDLimit:         50.0,
		SoftThresholdPct: 75,
		HardCapAction:    "block",
		UpdatedAt:        time.Now().UTC().Truncate(time.Second),
	}
	if err := s.SetBudget(b); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	got, err := s.GetBudget(team.ID, "month")
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	if got.TeamID != b.TeamID {
		t.Errorf("TeamID: want %q, got %q", b.TeamID, got.TeamID)
	}
	if got.Period != b.Period {
		t.Errorf("Period: want %q, got %q", b.Period, got.Period)
	}
	if got.USDLimit != b.USDLimit {
		t.Errorf("USDLimit: want %v, got %v", b.USDLimit, got.USDLimit)
	}
	if got.SoftThresholdPct != b.SoftThresholdPct {
		t.Errorf("SoftThresholdPct: want %d, got %d", b.SoftThresholdPct, got.SoftThresholdPct)
	}
	if got.HardCapAction != b.HardCapAction {
		t.Errorf("HardCapAction: want %q, got %q", b.HardCapAction, got.HardCapAction)
	}
}

func TestSetBudget_Idempotent(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("idm", "Idm Corp")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	b1 := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 10.0,
		SoftThresholdPct: 80, HardCapAction: "warn", UpdatedAt: time.Now().UTC(),
	}
	if err := s.SetBudget(b1); err != nil {
		t.Fatalf("SetBudget first: %v", err)
	}
	b2 := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 20.0,
		SoftThresholdPct: 90, HardCapAction: "block", UpdatedAt: time.Now().UTC(),
	}
	if err := s.SetBudget(b2); err != nil {
		t.Fatalf("SetBudget second: %v", err)
	}
	got, err := s.GetBudget(team.ID, "day")
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	if got.USDLimit != 20.0 {
		t.Errorf("want updated USDLimit=20, got %v", got.USDLimit)
	}
	if got.SoftThresholdPct != 90 {
		t.Errorf("want updated SoftThresholdPct=90, got %d", got.SoftThresholdPct)
	}
	if got.HardCapAction != "block" {
		t.Errorf("want updated HardCapAction=block, got %q", got.HardCapAction)
	}
}

func TestListBudgets(t *testing.T) {
	s := openTest(t)
	t1, err := s.AddTeam("t1", "Team One")
	if err != nil {
		t.Fatalf("AddTeam t1: %v", err)
	}
	t2, err := s.AddTeam("t2", "Team Two")
	if err != nil {
		t.Fatalf("AddTeam t2: %v", err)
	}
	for _, b := range []Budget{
		{TeamID: t1.ID, Period: "day", USDLimit: 5, SoftThresholdPct: 80, HardCapAction: "block", UpdatedAt: time.Now().UTC()},
		{TeamID: t1.ID, Period: "month", USDLimit: 100, SoftThresholdPct: 80, HardCapAction: "block", UpdatedAt: time.Now().UTC()},
		{TeamID: t2.ID, Period: "month", USDLimit: 200, SoftThresholdPct: 70, HardCapAction: "warn", UpdatedAt: time.Now().UTC()},
	} {
		if err := s.SetBudget(b); err != nil {
			t.Fatalf("SetBudget: %v", err)
		}
	}

	all, err := s.ListBudgets("")
	if err != nil {
		t.Fatalf("ListBudgets all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("want 3 budgets total, got %d", len(all))
	}

	byT1, err := s.ListBudgets(t1.ID)
	if err != nil {
		t.Fatalf("ListBudgets t1: %v", err)
	}
	if len(byT1) != 2 {
		t.Errorf("want 2 budgets for t1, got %d", len(byT1))
	}

	byT2, err := s.ListBudgets(t2.ID)
	if err != nil {
		t.Fatalf("ListBudgets t2: %v", err)
	}
	if len(byT2) != 1 {
		t.Errorf("want 1 budget for t2, got %d", len(byT2))
	}
}

func TestGetBudget_NotFound(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("nf", "No Budget")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	_, err = s.GetBudget(team.ID, "day")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCheckBudget_NoBudget(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("nb", "No Budget")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	status, err := s.CheckBudget(team.ID, "day", time.Now().UTC())
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.HardBreached {
		t.Error("want HardBreached=false when no budget configured")
	}
	if status.SoftBreached {
		t.Error("want SoftBreached=false when no budget configured")
	}
}

func TestCheckBudget_UnderLimit(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("ul", "Under Limit")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s.IssueAPIKey(team.ID, "inbound", "")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	now := time.Now().UTC()
	day := dayUTC(now)
	// spend $0.10 = 100_000 micros, limit $1.00
	if err := s.CommitUsage(ak.ID, day, 0, 0, 0, 100_000); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}
	budget := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 1.0,
		SoftThresholdPct: 80, HardCapAction: "block", UpdatedAt: now,
	}
	if err := s.SetBudget(budget); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	status, err := s.CheckBudget(team.ID, "day", now)
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.HardBreached {
		t.Errorf("want HardBreached=false, spent=%.4f limit=%.4f", status.SpentUSD, status.LimitUSD)
	}
	if status.SoftBreached {
		t.Errorf("want SoftBreached=false (10%% < 80%%), got pct=%.1f", status.UsedPct)
	}
}

func TestCheckBudget_SoftBreach(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("sb", "Soft Breach")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s.IssueAPIKey(team.ID, "inbound", "")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	now := time.Now().UTC()
	day := dayUTC(now)
	// spend $0.85 = 850_000 micros, limit $1.00 → 85% → soft breach at 80%
	if err := s.CommitUsage(ak.ID, day, 0, 0, 0, 850_000); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}
	budget := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 1.0,
		SoftThresholdPct: 80, HardCapAction: "warn", UpdatedAt: now,
	}
	if err := s.SetBudget(budget); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	status, err := s.CheckBudget(team.ID, "day", now)
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if !status.SoftBreached {
		t.Errorf("want SoftBreached=true at 85%%, got pct=%.1f", status.UsedPct)
	}
	if status.HardBreached {
		t.Error("want HardBreached=false (< 100%)")
	}
}

func TestCheckBudget_HardBreach(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("hb", "Hard Breach")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s.IssueAPIKey(team.ID, "inbound", "")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	now := time.Now().UTC()
	day := dayUTC(now)
	// spend $1.20 = 1_200_000 micros, limit $1.00 → 120% → hard breach with action=block
	if err := s.CommitUsage(ak.ID, day, 0, 0, 0, 1_200_000); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}
	budget := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 1.0,
		SoftThresholdPct: 80, HardCapAction: "block", UpdatedAt: now,
	}
	if err := s.SetBudget(budget); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	status, err := s.CheckBudget(team.ID, "day", now)
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if !status.SoftBreached {
		t.Error("want SoftBreached=true at 120%%")
	}
	if !status.HardBreached {
		t.Errorf("want HardBreached=true, spent=%.4f limit=%.4f pct=%.1f action=%s",
			status.SpentUSD, status.LimitUSD, status.UsedPct, status.Action)
	}
	if status.Action != "block" {
		t.Errorf("want Action=block, got %q", status.Action)
	}
}

func TestCheckBudget_HardBreach_WarnAction_NotBlocked(t *testing.T) {
	s := openTest(t)
	team, err := s.AddTeam("hw", "Hard Warn")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s.IssueAPIKey(team.ID, "inbound", "")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	now := time.Now().UTC()
	day := dayUTC(now)
	// overspend but action=warn → HardBreached should be false (only block triggers hard)
	if err := s.CommitUsage(ak.ID, day, 0, 0, 0, 2_000_000); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}
	budget := Budget{
		TeamID: team.ID, Period: "day", USDLimit: 1.0,
		SoftThresholdPct: 80, HardCapAction: "warn", UpdatedAt: now,
	}
	if err := s.SetBudget(budget); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	status, err := s.CheckBudget(team.ID, "day", now)
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.HardBreached {
		t.Error("want HardBreached=false when action=warn (warn never blocks)")
	}
	if !status.SoftBreached {
		t.Error("want SoftBreached=true at 200%%")
	}
}
