package mcp

import (
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// TestSetModelAlias_ModeCognitive: the MCP tool accepts an optional `mode`
// param. When set to "cognitive", the stored alias has Mode=="cognitive".
func TestSetModelAlias_ModeCognitive(t *testing.T) {
	st := openTestStore(t)
	// Provider FK target.
	if err := st.AddProvider(store.Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	h := setModelAliasHandler(st)
	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"alias":          "smart",
		"provider_name":  "deepseek",
		"upstream_model": "deepseek-v4-pro",
		"mode":           "cognitive",
	}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool err: %v", res.Content)
	}

	a, err := st.ResolveAlias("smart")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if a.Mode != "cognitive" {
		t.Errorf("Mode: want cognitive, got %q", a.Mode)
	}
}

// TestSetModelAlias_ModeDefaultStatic: when the caller omits mode, the tool
// stores "static" so existing automation keeps working.
func TestSetModelAlias_ModeDefaultStatic(t *testing.T) {
	st := openTestStore(t)
	if err := st.AddProvider(store.Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	h := setModelAliasHandler(st)
	if _, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"alias":          "fast",
		"provider_name":  "deepseek",
		"upstream_model": "deepseek-v4-flash",
	})); err != nil {
		t.Fatalf("handler: %v", err)
	}
	a, _ := st.ResolveAlias("fast")
	if a.Mode != "static" {
		t.Errorf("default Mode: want static, got %q", a.Mode)
	}
}
