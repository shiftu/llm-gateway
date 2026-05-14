package mcp

// tenancy_tools_test.go covers T9: team + API-key + quota MCP tool handlers.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// --- add_team ---

func TestAddTeam_Success(t *testing.T) {
	st := openTestStore(t)
	h := addTeamHandler(st)
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"slug": "acme", "name": "Acme Corp",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("handler error: %v", res.Content)
	}
	text := textContent(t, res)
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["slug"] != "acme" {
		t.Errorf("want slug=acme, got %v", m["slug"])
	}
	team, err := st.GetTeamBySlug("acme")
	if err != nil {
		t.Fatalf("GetTeamBySlug: %v", err)
	}
	if team.Name != "Acme Corp" {
		t.Errorf("stored name mismatch: %q", team.Name)
	}
}

func TestAddTeam_Duplicate_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := addTeamHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"slug": "acme", "name": "Another Acme",
	}))
	if !res.IsError {
		t.Error("expected tool error for duplicate slug")
	}
}

func TestAddTeam_InvalidSlug_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := addTeamHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"slug": "INVALID SLUG!", "name": "Bad",
	}))
	if !res.IsError {
		t.Error("expected tool error for invalid slug")
	}
}

func TestAddTeam_InsufficientScope_Denied(t *testing.T) {
	st := openTestStore(t)
	h := addTeamHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"slug": "acme", "name": "Acme Corp",
	}))
	if !res.IsError {
		t.Error("expected permission denied for mcp_admin scope")
	}
}

// --- list_teams ---

func TestListTeams_ReturnsAll(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("alpha", "Alpha")
	_, _ = st.AddTeam("beta", "Beta")
	h := listTeamsHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(nil))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(textContent(t, res)), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 teams, got %d", len(rows))
	}
}

func TestListTeams_Empty(t *testing.T) {
	st := openTestStore(t)
	h := listTeamsHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(nil))
	if textContent(t, res) != "[]" {
		t.Error("expected empty array")
	}
}

// --- issue_api_key ---

func TestIssueAPIKey_Success(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := issueAPIKeyHandler(st)
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"team_slug": "acme", "scope": "inbound", "label": "ci-runner",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("handler error: %v", res.Content)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(textContent(t, res)), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	token, _ := m["token"].(string)
	if !strings.HasPrefix(token, "lgw_") {
		t.Errorf("expected lgw_ token, got %q", token)
	}
	if m["scope"] != "inbound" {
		t.Errorf("want scope=inbound, got %v", m["scope"])
	}
}

func TestIssueAPIKey_TeamNotFound_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := issueAPIKeyHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"team_slug": "ghost", "scope": "inbound",
	}))
	if !res.IsError {
		t.Error("expected tool error for unknown team")
	}
}

func TestIssueAPIKey_InvalidScope_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := issueAPIKeyHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"team_slug": "acme", "scope": "superadmin",
	}))
	if !res.IsError {
		t.Error("expected tool error for invalid scope")
	}
}

// --- revoke_api_key ---

func TestRevokeAPIKey_Success(t *testing.T) {
	st := openTestStore(t)
	team, _ := st.AddTeam("acme", "Acme")
	ak, _, _ := st.IssueAPIKey(team.ID, "inbound", "test")
	h := revokeAPIKeyHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{"key_id": ak.ID}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if _, err := st.LookupAPIKeyByPrefix(ak.Prefix); err == nil {
		t.Error("revoked key should not be found by prefix lookup")
	}
}

func TestRevokeAPIKey_NotFound_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := revokeAPIKeyHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{"key_id": "ak_deadbeef"}))
	if !res.IsError {
		t.Error("expected tool error for unknown key id")
	}
}

// --- list_api_keys ---

func TestListAPIKeys_ByTeam(t *testing.T) {
	st := openTestStore(t)
	team, _ := st.AddTeam("acme", "Acme")
	st.IssueAPIKey(team.ID, "inbound", "k1")
	st.IssueAPIKey(team.ID, "inbound", "k2")
	h := listAPIKeysHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"team_slug": "acme"}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(textContent(t, res)), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 keys, got %d", len(rows))
	}
	for _, r := range rows {
		if _, hasHash := r["hash"]; hasHash {
			t.Error("hash must not appear in list_api_keys output")
		}
	}
}

func TestListAPIKeys_TeamNotFound_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := listAPIKeysHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"team_slug": "nope"}))
	if !res.IsError {
		t.Error("expected tool error for unknown team")
	}
}

// --- set_quota / get_quota / list_quotas ---

func TestSetQuota_TeamScope_Success(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := setQuotaHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"scope_kind":   "team",
		"scope_id":     "acme",
		"window":       "month",
		"max_requests": float64(10000),
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
}

func TestSetQuota_MissingLimits_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := setQuotaHandler(st)
	res, _ := h(ctxWithScope("mcp_super"), callTool(map[string]any{
		"scope_kind": "team", "scope_id": "acme", "window": "month",
	}))
	if !res.IsError {
		t.Error("expected tool error when all limits omitted")
	}
}

func TestGetQuota_Success(t *testing.T) {
	st := openTestStore(t)
	team, _ := st.AddTeam("acme", "Acme")
	n := int64(500)
	_ = st.SetQuota(store.Quota{ScopeKind: "team", ScopeID: team.ID, Window: "day", MaxRequests: &n})
	h := getQuotaHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"scope_kind": "team", "scope_id": "acme", "window": "day",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(textContent(t, res)), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["max_requests"] != float64(500) {
		t.Errorf("want max_requests=500, got %v", m["max_requests"])
	}
}

func TestGetQuota_NotFound_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")
	h := getQuotaHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"scope_kind": "team", "scope_id": "acme", "window": "minute",
	}))
	if !res.IsError {
		t.Error("expected tool error for missing quota")
	}
}

func TestListQuotas_ReturnsAllWindows(t *testing.T) {
	st := openTestStore(t)
	team, _ := st.AddTeam("acme", "Acme")
	n := int64(100)
	_ = st.SetQuota(store.Quota{ScopeKind: "team", ScopeID: team.ID, Window: "day", MaxRequests: &n})
	_ = st.SetQuota(store.Quota{ScopeKind: "team", ScopeID: team.ID, Window: "month", MaxRequests: &n})
	h := listQuotasHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"scope_kind": "team", "scope_id": "acme",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(textContent(t, res)), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 quota rows, got %d", len(rows))
	}
}
