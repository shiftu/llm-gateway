package mcp

// resources_test.go — TDD for T9: lgw:// MCP resource handlers.
//
// Handlers are called directly (not through MCP wire protocol) using the same
// pattern as fallback_tools_test.go. Auth is tested via ctxWithScope (from
// acl_test.go) and context.Background() for unauthenticated cases.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/panda/llm-gateway/internal/store"
)

// readResource builds a ReadResourceRequest with the given URI.
func readResource(uri string) mcplib.ReadResourceRequest {
	return mcplib.ReadResourceRequest{
		Params: mcplib.ReadResourceParams{
			URI: uri,
		},
	}
}

// textOfResource extracts the text from the first TextResourceContents in a
// resource handler result.
func textOfResource(t *testing.T, contents []mcplib.ResourceContents) string {
	t.Helper()
	if len(contents) == 0 {
		t.Fatal("resource handler returned no contents")
	}
	tc, ok := contents[0].(mcplib.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents, got %T", contents[0])
	}
	return tc.Text
}

// --- lgw://teams/{id}/usage ---

func TestResourceTeamUsage_Auth_Rejected(t *testing.T) {
	st := openTestStore(t)
	h := teamUsageHandler(st)
	_, err := h(context.Background(), readResource("lgw://teams/tm_abc123/usage"))
	if err == nil {
		t.Fatal("expected auth error for unauthenticated caller")
	}
}

func TestResourceTeamUsage_HappyPath(t *testing.T) {
	st := openTestStore(t)

	// Create a team and an API key for it.
	team, err := st.AddTeam("acme", "Acme Corp")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := st.IssueAPIKey(team.ID, "inbound", "test-key")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	// Seed usage for today.
	today := todayUTC()
	if err := st.CommitUsage(ak.ID, today, 100, 50, 10, 0, 5000); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}

	h := teamUsageHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://teams/"+team.ID+"/usage"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)

	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["team_id"] != team.ID {
		t.Errorf("team_id: want %q, got %v", team.ID, m["team_id"])
	}
	if m["input_tokens"].(float64) != 100 {
		t.Errorf("input_tokens: want 100, got %v", m["input_tokens"])
	}
	if m["output_tokens"].(float64) != 50 {
		t.Errorf("output_tokens: want 50, got %v", m["output_tokens"])
	}
	if m["reasoning_tokens"].(float64) != 10 {
		t.Errorf("reasoning_tokens: want 10, got %v", m["reasoning_tokens"])
	}
	if m["cost_micros"].(float64) != 5000 {
		t.Errorf("cost_micros: want 5000, got %v", m["cost_micros"])
	}
}

func TestResourceTeamUsage_UnknownTeam_ReturnsZeros(t *testing.T) {
	st := openTestStore(t)
	h := teamUsageHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://teams/tm_unknown/usage"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Unknown team → no keys → all zeros, no error.
	if m["input_tokens"].(float64) != 0 {
		t.Errorf("expected 0 input_tokens for unknown team, got %v", m["input_tokens"])
	}
}

// --- lgw://teams/{id}/quota ---

func TestResourceTeamQuota_Auth_Rejected(t *testing.T) {
	st := openTestStore(t)
	h := teamQuotaHandler(st)
	_, err := h(context.Background(), readResource("lgw://teams/tm_abc123/quota"))
	if err == nil {
		t.Fatal("expected auth error for unauthenticated caller")
	}
}

func TestResourceTeamQuota_HappyPath(t *testing.T) {
	st := openTestStore(t)

	team, err := st.AddTeam("beta", "Beta Inc")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	maxReq := int64(1000)
	if err := st.SetQuota(store.Quota{
		ScopeKind: "team", ScopeID: team.ID, Window: "day",
		MaxRequests: &maxReq,
	}); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	h := teamQuotaHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://teams/"+team.ID+"/quota"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)

	var quotas []map[string]any
	if err := json.Unmarshal([]byte(body), &quotas); err != nil {
		t.Fatalf("unmarshal: %v %q", err, body)
	}
	if len(quotas) != 1 {
		t.Fatalf("want 1 quota row, got %d", len(quotas))
	}
	if quotas[0]["window"] != "day" {
		t.Errorf("window: want 'day', got %v", quotas[0]["window"])
	}
	if quotas[0]["max_requests"].(float64) != 1000 {
		t.Errorf("max_requests: want 1000, got %v", quotas[0]["max_requests"])
	}
}

func TestResourceTeamQuota_EmptyQuota_ReturnsEmptyArray(t *testing.T) {
	st := openTestStore(t)
	team, _ := st.AddTeam("gamma", "Gamma LLC")
	h := teamQuotaHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://teams/"+team.ID+"/quota"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)
	if body != "[]" {
		t.Errorf("want '[]', got %q", body)
	}
}

// --- lgw://providers ---

func TestResourceProviders_Auth_Rejected(t *testing.T) {
	st := openTestStore(t)
	h := providersHandler(st)
	_, err := h(context.Background(), readResource("lgw://providers"))
	if err == nil {
		t.Fatal("expected auth error for unauthenticated caller")
	}
}

func TestResourceProviders_HappyPath(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	if err := st.AddProvider(store.Provider{
		Name:             "anthropic",
		Kind:             "anthropic",
		APIKey:           "sk-ant-test",
		AnthropicBaseURL: "https://api.anthropic.com",
	}); err != nil {
		t.Fatalf("AddProvider(anthropic): %v", err)
	}

	h := providersHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)

	var providers []map[string]any
	if err := json.Unmarshal([]byte(body), &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("want 2 providers, got %d", len(providers))
	}
	// Should be sorted by name: anthropic < deepseek
	if providers[0]["name"] != "anthropic" {
		t.Errorf("first provider: want anthropic, got %v", providers[0]["name"])
	}
	if providers[1]["name"] != "deepseek" {
		t.Errorf("second provider: want deepseek, got %v", providers[1]["name"])
	}
	// Verify schema fields present
	for _, p := range providers {
		for _, field := range []string{"name", "kind", "has_openai_url", "has_anthropic_url"} {
			if _, ok := p[field]; !ok {
				t.Errorf("provider missing field %q: %v", field, p)
			}
		}
	}
	// deepseek has openai url, anthropic has anthropic url
	deepseek := providers[1]
	if deepseek["has_openai_url"] != true {
		t.Errorf("deepseek should have_openai_url=true, got %v", deepseek["has_openai_url"])
	}
	anthropicP := providers[0]
	if anthropicP["has_anthropic_url"] != true {
		t.Errorf("anthropic should have_anthropic_url=true, got %v", anthropicP["has_anthropic_url"])
	}
}

func TestResourceProviders_Empty_ReturnsEmptyArray(t *testing.T) {
	st := openTestStore(t)
	h := providersHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)
	if body != "[]" {
		t.Errorf("want '[]', got %q", body)
	}
}

// --- lgw://providers/{name}/health ---

func TestResourceProviderHealth_Auth_Rejected(t *testing.T) {
	st := openTestStore(t)
	h := providerHealthHandler(st)
	_, err := h(context.Background(), readResource("lgw://providers/deepseek/health"))
	if err == nil {
		t.Fatal("expected auth error for unauthenticated caller")
	}
}

func TestResourceProviderHealth_ReturnsPlaceholder(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	h := providerHealthHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers/deepseek/health"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)
	// The health handler returns a placeholder or live data — either way must
	// be valid JSON with a "name" field.
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal health response: %v — body: %q", err, body)
	}
	if m["name"] != "deepseek" {
		t.Errorf("name: want 'deepseek', got %v", m["name"])
	}
}

func TestResourceProviderHealth_UnknownProvider(t *testing.T) {
	st := openTestStore(t)
	h := providerHealthHandler(st)
	_, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers/ghost/health"))
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention provider name; got %q", err.Error())
	}
}

// --- lgw://providers/{name}/capabilities ---

func TestResourceProviderCapabilities_Auth_Rejected(t *testing.T) {
	st := openTestStore(t)
	h := providerCapabilitiesHandler(st)
	_, err := h(context.Background(), readResource("lgw://providers/deepseek/capabilities"))
	if err == nil {
		t.Fatal("expected auth error for unauthenticated caller")
	}
}

func TestResourceProviderCapabilities_HappyPath(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	if err := st.SetProviderCapability("deepseek", "streaming", "true"); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}
	if err := st.SetProviderCapability("deepseek", "max_context", "128k"); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}

	h := providerCapabilitiesHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers/deepseek/capabilities"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)

	var caps []map[string]any
	if err := json.Unmarshal([]byte(body), &caps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("want 2 capabilities, got %d", len(caps))
	}
	// Sorted by capability name: max_context < streaming
	if caps[0]["capability"] != "max_context" {
		t.Errorf("first cap: want max_context, got %v", caps[0]["capability"])
	}
	if caps[0]["value"] != "128k" {
		t.Errorf("value: want '128k', got %v", caps[0]["value"])
	}
	// Verify schema fields
	for _, c := range caps {
		for _, field := range []string{"capability", "value", "updated_at"} {
			if _, ok := c[field]; !ok {
				t.Errorf("capability missing field %q: %v", field, c)
			}
		}
	}
}

func TestResourceProviderCapabilities_Empty_ReturnsEmptyArray(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	h := providerCapabilitiesHandler(st)
	contents, err := h(ctxWithScope("mcp_auditor"), readResource("lgw://providers/deepseek/capabilities"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := textOfResource(t, contents)
	if body != "[]" {
		t.Errorf("want '[]', got %q", body)
	}
}

// --- URI parsing helper ---

func TestParseURISegment(t *testing.T) {
	cases := []struct {
		uri   string
		idx   int
		want  string
	}{
		{"lgw://teams/tm_abc123/usage", 3, "tm_abc123"},
		{"lgw://teams/tm_abc123/quota", 4, "quota"},
		{"lgw://providers/deepseek/health", 3, "deepseek"},
		{"lgw://providers", 2, "providers"},
	}
	for _, tc := range cases {
		got := uriSegment(tc.uri, tc.idx)
		if got != tc.want {
			t.Errorf("uriSegment(%q, %d): want %q, got %q", tc.uri, tc.idx, tc.want, got)
		}
	}
}

// --- helpers ---

// todayUTC returns the ms-epoch of 00:00 UTC for today.
func todayUTC() int64 {
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return midnight.UnixMilli()
}
