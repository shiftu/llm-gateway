package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/health"
)

// RegisterHealthTools adds get_provider_health to s. Pass nil for mgr when
// health probes are disabled (LLM_GATEWAY_HEALTH_PROBES unset) — the tool is
// still registered so agents discover the feature; calling it returns a
// "probes disabled" error pointing at the env var.
func RegisterHealthTools(s *mcpserver.MCPServer, mgr *health.Manager) {
	s.AddTool(
		mcplib.NewTool("get_provider_health",
			mcplib.WithDescription("Return the rolling health snapshot for a provider — latency, last status, sample count. Probes must be enabled via LLM_GATEWAY_HEALTH_PROBES=1."),
			mcplib.WithString("name", mcplib.Description("Provider slug whose snapshot to return")),
		),
		getProviderHealthHandler(mgr),
	)
}

func getProviderHealthHandler(mgr *health.Manager) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "get_provider_health"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		if mgr == nil {
			return mcplib.NewToolResultError(
				"health probes are disabled. Set LLM_GATEWAY_HEALTH_PROBES=1 and restart the gateway.",
			), nil
		}
		name := req.GetString("name", "")
		if name == "" {
			return mcplib.NewToolResultError("required parameter missing: name"), nil
		}
		snap, ok := mgr.Snapshot(name)
		if !ok {
			return mcplib.NewToolResultError(
				fmt.Sprintf("unknown provider: %q (not registered for health probes)", name),
			), nil
		}
		b, _ := json.Marshal(snap)
		return mcplib.NewToolResultText(string(b)), nil
	}
}
