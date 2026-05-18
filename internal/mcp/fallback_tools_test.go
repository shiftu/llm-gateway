package mcp

import (
	"encoding/json"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func TestFallbackTools_SetListRemove(t *testing.T) {
	st := openTestStore(t)
	setH := setFallbackPolicyHandler(st)
	listH := listFallbackPoliciesHandler(st)
	delH := removeFallbackPolicyHandler(st)

	// set_fallback_policy
	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"trigger": "http_5xx", "action": "next_best", "max_chain_depth": float64(3),
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set_fallback_policy error: %v", res.Content)
	}
	var created fallbackPolicyJSON
	if err := json.Unmarshal([]byte(textOf(t, res)), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID <= 0 {
		t.Fatalf("expected id > 0, got %d", created.ID)
	}
	if created.Trigger != "http_5xx" || created.Action != "next_best" {
		t.Errorf("round-trip mismatch: %+v", created)
	}

	// list_fallback_policies
	res2, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": ""}))
	if err != nil {
		t.Fatalf("list handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("list_fallback_policies error: %v", res2.Content)
	}
	var policies []fallbackPolicyJSON
	if err := json.Unmarshal([]byte(textOf(t, res2)), &policies); err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 {
		t.Fatalf("want 1 policy, got %d", len(policies))
	}

	// remove_fallback_policy
	res3, err := delH(ctxWithScope("mcp_admin"), callTool(map[string]any{"id": float64(created.ID)}))
	if err != nil {
		t.Fatalf("remove handler error: %v", err)
	}
	if res3.IsError {
		t.Fatalf("remove_fallback_policy error: %v", res3.Content)
	}

	res4, _ := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": ""}))
	var after []fallbackPolicyJSON
	_ = json.Unmarshal([]byte(textOf(t, res4)), &after)
	if len(after) != 0 {
		t.Errorf("expected empty after remove, got %d", len(after))
	}
}

func TestFallbackTools_SpecificProvider(t *testing.T) {
	st := openTestStore(t)
	setH := setFallbackPolicyHandler(st)

	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"trigger": "http_429", "action": "specific_provider",
		"target_provider": "backup", "max_chain_depth": float64(2),
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set_fallback_policy error: %v", res.Content)
	}
	var p fallbackPolicyJSON
	_ = json.Unmarshal([]byte(textOf(t, res)), &p)
	if p.TargetProvider != "backup" {
		t.Errorf("target_provider: want backup, got %q", p.TargetProvider)
	}
}

func TestFallbackTools_TeamScoped(t *testing.T) {
	st := openTestStore(t)
	team, err := st.AddTeam("acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	setH := setFallbackPolicyHandler(st)
	listH := listFallbackPoliciesHandler(st)

	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"team_id": team.ID, "trigger": "http_5xx",
		"action": "next_best", "max_chain_depth": float64(1),
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set_fallback_policy error: %v", res.Content)
	}

	res2, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": team.ID}))
	if err != nil {
		t.Fatalf("list handler error: %v", err)
	}
	var policies []fallbackPolicyJSON
	_ = json.Unmarshal([]byte(textOf(t, res2)), &policies)
	if len(policies) != 1 || policies[0].TeamID != team.ID {
		t.Errorf("expected 1 team-scoped policy, got %+v", policies)
	}
}

func TestFallbackTools_ACL_AuditorCanList(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.SetFallbackPolicy(store.FallbackPolicy{
		Trigger: "http_5xx", Action: "next_best", MaxChainDepth: 3,
	})
	listH := listFallbackPoliciesHandler(st)

	res, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": ""}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Errorf("auditor should be able to list: %v", res.Content)
	}
}

func TestFallbackTools_ACL_AuditorCannotSet(t *testing.T) {
	st := openTestStore(t)
	setH := setFallbackPolicyHandler(st)

	res, err := setH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"trigger": "http_5xx", "action": "next_best",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Error("auditor should not be able to set policy")
	}
}
