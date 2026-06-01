package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/panda/llm-gateway/internal/health"
)

// fakeProbe returns a fixed-result ProbeFn used for handler tests.
func fakeProbe(latencyMs int64, healthy bool) health.ProbeFn {
	return func(_ context.Context, _, _ string) health.ProbeResult {
		return health.ProbeResult{Healthy: healthy, LatencyMs: latencyMs, StatusCode: 200}
	}
}

// TestGetProviderHealth_SingleProvider: with a {name} argument, the handler
// returns the snapshot for that one provider as JSON. Caller needs the
// mcp_auditor scope (this is a read-only observability tool).
func TestGetProviderHealth_SingleProvider(t *testing.T) {
	mgr := health.NewManager(fakeProbe(42, true), time.Minute)
	mgr.Register("deepseek", "https://api.deepseek.com/v1/models", "")
	if err := mgr.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	h := getProviderHealthHandler(mgr)
	ctx := ctxWithScope("mcp_auditor")
	res, err := h(ctx, callTool(map[string]any{"name": "deepseek"}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	body := textOf(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%q", err, body)
	}
	if got["name"] != "deepseek" {
		t.Errorf("name: want deepseek, got %v", got["name"])
	}
	if got["last_healthy"] != true {
		t.Errorf("last_healthy: want true, got %v", got["last_healthy"])
	}
	if got["last_latency_ms"].(float64) != 42 {
		t.Errorf("last_latency_ms: want 42, got %v", got["last_latency_ms"])
	}
	if got["sample_count"].(float64) != 1 {
		t.Errorf("sample_count: want 1, got %v", got["sample_count"])
	}
}

// TestGetProviderHealth_UnknownProvider: a name not in the manager yields a
// tool-level error (not a transport error). The MCP layer renders this as
// a structured error response the agent can react to.
func TestGetProviderHealth_UnknownProvider(t *testing.T) {
	mgr := health.NewManager(fakeProbe(1, true), time.Minute)
	h := getProviderHealthHandler(mgr)
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{"name": "ghost"}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for unknown provider, got success: %v", res.Content)
	}
	if !strings.Contains(textOf(t, res), "ghost") {
		t.Errorf("error should mention the unknown name; got %q", textOf(t, res))
	}
}

// TestGetProviderHealth_DisabledManager: when probes are not enabled
// (env LLM_GATEWAY_HEALTH_PROBES unset → no Manager), the handler must
// return a clear "probes disabled" error so agents know the feature is off
// rather than misinterpreting empty results as "all providers down".
func TestGetProviderHealth_DisabledManager(t *testing.T) {
	h := getProviderHealthHandler(nil)
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when Manager is nil, got success: %v", res.Content)
	}
	if !strings.Contains(textOf(t, res), "LLM_GATEWAY_HEALTH_PROBES") {
		t.Errorf("error should mention the env var; got %q", textOf(t, res))
	}
}

// textOf extracts the first TextContent string from a CallToolResult.
// Mirrors the pattern in provider_tools_test.go.
func textOf(t *testing.T, res *mcplib.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("CallToolResult has no Content")
	}
	tc, ok := res.Content[0].(mcplib.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}
