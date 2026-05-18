package mcp

import (
	"encoding/json"
	"testing"
)

func TestSetBudget_Success(t *testing.T) {
	st := openTestStore(t)
	team, err := st.AddTeam("acme", "Acme Corp")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	h := setBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"team_id":   team.ID,
		"period":    "day",
		"usd_limit": float64(10),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got: %v", res.Content)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "ok" {
		t.Errorf("status: want ok, got %v", out["status"])
	}
	if out["team_id"] != team.ID {
		t.Errorf("team_id: want %q, got %v", team.ID, out["team_id"])
	}
}

func TestSetBudget_InvalidPeriod(t *testing.T) {
	st := openTestStore(t)
	h := setBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"team_id":   "tm_fake",
		"period":    "week",
		"usd_limit": float64(10),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for invalid period")
	}
}

func TestSetBudget_MissingTeamID(t *testing.T) {
	st := openTestStore(t)
	h := setBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"period":    "day",
		"usd_limit": float64(5),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for missing team_id")
	}
}

func TestSetBudget_ACL_AuditorDenied(t *testing.T) {
	st := openTestStore(t)
	h := setBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"team_id":   "tm_fake",
		"period":    "day",
		"usd_limit": float64(10),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mcp_auditor should be denied set_budget")
	}
}

func TestCheckBudget_NoBudget(t *testing.T) {
	st := openTestStore(t)
	team, err := st.AddTeam("empty", "Empty Team")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	h := checkBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"team_id": team.ID,
		"period":  "day",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	// When no budget is configured CheckBudget returns a zero BudgetStatus (no limit)
	if res.IsError {
		t.Fatalf("expected success (no-budget = unlimited), got error: %v", res.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["limit_usd"].(float64) != 0 {
		t.Errorf("limit_usd: want 0 (no budget), got %v", out["limit_usd"])
	}
	if out["hard_breached"].(bool) {
		t.Errorf("hard_breached should be false when no budget configured")
	}
}

func TestCheckBudget_WithBudget(t *testing.T) {
	st := openTestStore(t)
	team, err := st.AddTeam("billed", "Billed Team")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	setH := setBudgetHandler(st)
	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"team_id":            team.ID,
		"period":             "month",
		"usd_limit":          float64(100),
		"soft_threshold_pct": float64(75),
		"hard_cap_action":    "block",
	}))
	if err != nil {
		t.Fatalf("setBudget handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("setBudget failed: %v", res.Content)
	}

	checkH := checkBudgetHandler(st)
	res, err = checkH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"team_id": team.ID,
		"period":  "month",
	}))
	if err != nil {
		t.Fatalf("checkBudget handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("checkBudget failed: %v", res.Content)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["team_id"] != team.ID {
		t.Errorf("team_id: want %q, got %v", team.ID, out["team_id"])
	}
	if out["period"] != "month" {
		t.Errorf("period: want month, got %v", out["period"])
	}
	if out["limit_usd"].(float64) != 100 {
		t.Errorf("limit_usd: want 100, got %v", out["limit_usd"])
	}
	if out["hard_breached"].(bool) {
		t.Errorf("hard_breached should be false (zero spend)")
	}
}

func TestCheckBudget_InvalidPeriod(t *testing.T) {
	st := openTestStore(t)
	h := checkBudgetHandler(st)

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"team_id": "tm_fake",
		"period":  "year",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for invalid period")
	}
}

func TestListBudgets_Empty(t *testing.T) {
	st := openTestStore(t)
	h := listBudgetsHandler(st)

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success: %v", res.Content)
	}

	var out []any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty array, got %d items", len(out))
	}
}

func TestListBudgets_AllTeams(t *testing.T) {
	st := openTestStore(t)
	t1, err := st.AddTeam("t1", "Team One")
	if err != nil {
		t.Fatalf("AddTeam t1: %v", err)
	}
	t2, err := st.AddTeam("t2", "Team Two")
	if err != nil {
		t.Fatalf("AddTeam t2: %v", err)
	}

	setH := setBudgetHandler(st)
	for _, args := range []map[string]any{
		{"team_id": t1.ID, "period": "day", "usd_limit": float64(5)},
		{"team_id": t1.ID, "period": "month", "usd_limit": float64(50)},
		{"team_id": t2.ID, "period": "day", "usd_limit": float64(20)},
	} {
		r, err := setH(ctxWithScope("mcp_admin"), callTool(args))
		if err != nil {
			t.Fatalf("setBudget error: %v", err)
		}
		if r.IsError {
			t.Fatalf("setBudget failed: %v", r.Content)
		}
	}

	listH := listBudgetsHandler(st)
	res, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success: %v", res.Content)
	}

	var out []map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 budgets, got %d", len(out))
	}
}

func TestListBudgets_FilterByTeam(t *testing.T) {
	st := openTestStore(t)
	t1, err := st.AddTeam("alpha", "Alpha")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	t2, err := st.AddTeam("beta", "Beta")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	setH := setBudgetHandler(st)
	for _, args := range []map[string]any{
		{"team_id": t1.ID, "period": "day", "usd_limit": float64(5)},
		{"team_id": t2.ID, "period": "day", "usd_limit": float64(10)},
	} {
		r, err := setH(ctxWithScope("mcp_admin"), callTool(args))
		if err != nil {
			t.Fatalf("setBudget: %v", err)
		}
		if r.IsError {
			t.Fatalf("setBudget failed: %v", r.Content)
		}
	}

	listH := listBudgetsHandler(st)
	res, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": t1.ID}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success: %v", res.Content)
	}

	var out []map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 budget for t1, got %d", len(out))
	}
	if out[0]["team_id"] != t1.ID {
		t.Errorf("team_id: want %q, got %v", t1.ID, out[0]["team_id"])
	}
}
