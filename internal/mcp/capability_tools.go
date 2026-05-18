package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterCapabilityTools adds set_provider_capability and
// list_provider_capabilities to the MCP server (v0.3 T11).
func RegisterCapabilityTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("set_provider_capability",
			mcplib.WithDescription("Upsert a capability tag for a provider (e.g. streaming=true, context_length=128k). Value is optional; presence alone gates routing eligibility."),
			mcplib.WithString("provider_name", mcplib.Required(), mcplib.Description("Provider name")),
			mcplib.WithString("capability", mcplib.Required(), mcplib.Description("Capability key (e.g. 'streaming', 'function_calling', 'reasoning')")),
			mcplib.WithString("value", mcplib.Description("Optional value string (default '')")),
		),
		setProviderCapabilityHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_provider_capabilities",
			mcplib.WithDescription("List all capability tags set for a provider."),
			mcplib.WithString("provider_name", mcplib.Required(), mcplib.Description("Provider name")),
		),
		listProviderCapabilitiesHandler(st),
	)
}

type capabilityJSON struct {
	Capability string `json:"capability"`
	Value      string `json:"value"`
	UpdatedAt  string `json:"updated_at"`
}

func setProviderCapabilityHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_provider_capability"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		providerName := req.GetString("provider_name", "")
		if providerName == "" {
			return mcplib.NewToolResultError("required parameter missing: provider_name"), nil
		}
		capability := req.GetString("capability", "")
		if capability == "" {
			return mcplib.NewToolResultError("required parameter missing: capability"), nil
		}
		value := req.GetString("value", "")

		if err := st.SetProviderCapability(providerName, capability, value); err != nil {
			return mcplib.NewToolResultError("set_provider_capability failed: " + err.Error()), nil
		}
		return mcplib.NewToolResultText(
			fmt.Sprintf("capability %q set for provider %q", capability, providerName),
		), nil
	}
}

func listProviderCapabilitiesHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_provider_capabilities"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		providerName := req.GetString("provider_name", "")
		if providerName == "" {
			return mcplib.NewToolResultError("required parameter missing: provider_name"), nil
		}

		caps, err := st.ListProviderCapabilities(providerName)
		if err != nil {
			return mcplib.NewToolResultError("list_provider_capabilities failed: " + err.Error()), nil
		}
		if len(caps) == 0 {
			return mcplib.NewToolResultText(
				fmt.Sprintf("no capabilities set for provider %q", providerName),
			), nil
		}

		out := make([]capabilityJSON, len(caps))
		for i, c := range caps {
			out[i] = capabilityJSON{
				Capability: c.Capability,
				Value:      c.Value,
				UpdatedAt:  c.UpdatedAt,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}
