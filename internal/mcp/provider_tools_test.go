package mcp

// provider_tools_test.go covers T8: provider + model-alias MCP tool handlers.
// Each handler is tested directly (not through the MCP server wire) using
// an in-memory store and ctxWithScope helpers from acl_test.go.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// openTestStore opens an in-memory SQLite store and registers cleanup.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// callTool builds a minimal CallToolRequest with the given key-value pairs.
func callTool(args map[string]any) mcplib.CallToolRequest {
	return mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Arguments: args,
		},
	}
}

func addTestProvider(t *testing.T, st *store.Store, name string) {
	t.Helper()
	err := st.AddProvider(store.Provider{
		Name:          name,
		Kind:          "deepseek",
		APIKey:        "sk-test-" + name,
		OpenAIBaseURL: "https://api.deepseek.com",
	})
	if err != nil {
		t.Fatalf("addTestProvider(%s): %v", name, err)
	}
}

// --- add_provider ---

func TestAddProvider_Success(t *testing.T) {
	st := openTestStore(t)
	h := addProviderHandler(st)
	ctx := ctxWithScope("mcp_admin")
	req := callTool(map[string]any{
		"name":            "deepseek",
		"kind":            "deepseek",
		"api_key":         "sk-abc123",
		"openai_base_url": "https://api.deepseek.com",
	})
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("handler returned error: %v", res.Content)
	}
	// Verify provider is in the store.
	p, err := st.GetProvider("deepseek")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if p.APIKey != "sk-abc123" {
		t.Errorf("stored api_key mismatch: %q", p.APIKey)
	}
}

func TestAddProvider_Duplicate_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	h := addProviderHandler(st)
	ctx := ctxWithScope("mcp_admin")
	req := callTool(map[string]any{
		"name": "deepseek", "kind": "deepseek",
		"api_key": "sk-other", "openai_base_url": "https://api.deepseek.com",
	})
	res, _ := h(ctx, req)
	if !res.IsError {
		t.Error("expected tool-level error for duplicate provider")
	}
}

func TestAddProvider_MissingParams_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := addProviderHandler(st)
	ctx := ctxWithScope("mcp_admin")
	req := callTool(map[string]any{"name": "deepseek"}) // missing kind + api_key
	res, _ := h(ctx, req)
	if !res.IsError {
		t.Error("expected tool-level error for missing params")
	}
}

func TestAddProvider_InsufficientScope_Denied(t *testing.T) {
	st := openTestStore(t)
	h := addProviderHandler(st)
	req := callTool(map[string]any{
		"name": "x", "kind": "openai", "api_key": "k", "openai_base_url": "https://a.com",
	})
	res, _ := h(ctxWithScope("inbound"), req)
	if !res.IsError {
		t.Error("expected permission denied for inbound scope")
	}
}

func TestAddProvider_WithIsDefault_SetsDefault(t *testing.T) {
	st := openTestStore(t)
	h := addProviderHandler(st)
	ctx := ctxWithScope("mcp_super")
	req := callTool(map[string]any{
		"name": "ds", "kind": "deepseek", "api_key": "sk-x",
		"openai_base_url": "https://api.deepseek.com",
		"is_default":      true,
	})
	res, _ := h(ctx, req)
	if res.IsError {
		t.Fatalf("handler error: %v", res.Content)
	}
	def, err := st.GetDefaultProvider()
	if err != nil {
		t.Fatalf("GetDefaultProvider: %v", err)
	}
	if def.Name != "ds" {
		t.Errorf("expected default=ds, got %q", def.Name)
	}
}

// --- remove_provider ---

func TestRemoveProvider_Success(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "to-remove")
	h := removeProviderHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"name": "to-remove"}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if _, err := st.GetProvider("to-remove"); err == nil {
		t.Error("provider should have been deleted")
	}
}

func TestRemoveProvider_NotFound_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	h := removeProviderHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"name": "nope"}))
	if !res.IsError {
		t.Error("expected tool-level error for unknown provider")
	}
}

// --- list_providers ---

func TestListProviders_MasksAPIKey(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "p1")
	h := listProvidersHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(nil))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	text := textContent(t, res)
	if strings.Contains(text, "sk-test-p1") {
		t.Error("raw API key must not appear in list_providers output")
	}
	if !strings.Contains(text, "****") {
		t.Error("masked key marker '****' not found in output")
	}
}

func TestListProviders_Empty_ReturnsEmptyArray(t *testing.T) {
	st := openTestStore(t)
	h := listProvidersHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(nil))
	text := textContent(t, res)
	if text != "[]" {
		t.Errorf("expected '[]', got %q", text)
	}
}

// --- set_default_provider ---

func TestSetDefaultProvider_Success(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "a")
	addTestProvider(t, st, "b")
	h := setDefaultProviderHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"name": "b"}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	def, _ := st.GetDefaultProvider()
	if def.Name != "b" {
		t.Errorf("want default=b, got %q", def.Name)
	}
}

func TestSetDefaultProvider_NotFound(t *testing.T) {
	st := openTestStore(t)
	h := setDefaultProviderHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"name": "ghost"}))
	if !res.IsError {
		t.Error("expected tool error for unknown provider")
	}
}

// --- set_model_alias ---

func TestSetModelAlias_Success(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	h := setModelAliasHandler(st)
	req := callTool(map[string]any{
		"alias": "fast", "provider_name": "deepseek", "upstream_model": "deepseek-v4-flash",
	})
	res, _ := h(ctxWithScope("mcp_admin"), req)
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	a, err := st.ResolveAlias("fast")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if a.UpstreamModel != "deepseek-v4-flash" {
		t.Errorf("want deepseek-v4-flash, got %q", a.UpstreamModel)
	}
}

func TestSetModelAlias_Upsert_UpdatesExisting(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	h := setModelAliasHandler(st)
	ctx := ctxWithScope("mcp_admin")
	h(ctx, callTool(map[string]any{"alias": "fast", "provider_name": "deepseek", "upstream_model": "v1"}))
	h(ctx, callTool(map[string]any{"alias": "fast", "provider_name": "deepseek", "upstream_model": "v2"}))
	a, _ := st.ResolveAlias("fast")
	if a.UpstreamModel != "v2" {
		t.Errorf("upsert should update existing alias, got %q", a.UpstreamModel)
	}
}

func TestSetModelAlias_BadForeignKey_ReturnsToolError(t *testing.T) {
	st := openTestStore(t)
	// Provider "ghost" doesn't exist → FK violation
	h := setModelAliasHandler(st)
	req := callTool(map[string]any{
		"alias": "x", "provider_name": "ghost", "upstream_model": "m",
	})
	res, _ := h(ctxWithScope("mcp_admin"), req)
	if !res.IsError {
		t.Error("expected tool error for FK violation")
	}
}

// --- delete_model_alias ---

func TestDeleteModelAlias_Success(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	_ = st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)
	h := deleteModelAliasHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"alias": "fast"}))
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if _, err := st.ResolveAlias("fast"); err == nil {
		t.Error("alias should have been deleted")
	}
}

func TestDeleteModelAlias_NotFound(t *testing.T) {
	st := openTestStore(t)
	h := deleteModelAliasHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(map[string]any{"alias": "nope"}))
	if !res.IsError {
		t.Error("expected tool error for unknown alias")
	}
}

// --- list_model_aliases ---

func TestListModelAliases_ReturnsAll(t *testing.T) {
	st := openTestStore(t)
	addTestProvider(t, st, "deepseek")
	_ = st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)
	_ = st.SetAlias("smart", "deepseek", "deepseek-v4-pro", nil, nil)
	h := listModelAliasesHandler(st)
	res, _ := h(ctxWithScope("mcp_admin"), callTool(nil))
	text := textContent(t, res)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 aliases, got %d", len(rows))
	}
}

// --- maskKey ---

func TestMaskKey_LongerThan4(t *testing.T) {
	got := maskKey("sk-abcdef1234")
	if !strings.HasPrefix(got, "****") {
		t.Errorf("masked key should start with ****, got %q", got)
	}
	if !strings.HasSuffix(got, "1234") {
		t.Errorf("masked key should end with last 4 chars, got %q", got)
	}
}

func TestMaskKey_Short(t *testing.T) {
	if maskKey("ab") != "****" {
		t.Error("short key should be fully masked")
	}
}

// --- helpers ---

func textContent(t *testing.T, res *mcplib.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := res.Content[0].(mcplib.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

// Ensure context.Context import is used (ctxWithScope is in acl_test.go).
var _ context.Context
