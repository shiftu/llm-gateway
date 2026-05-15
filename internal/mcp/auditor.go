package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// auditorTools is the set of tool names visible to the mcp_auditor scope.
// These are read-only: list_*, get_*, ping, whoami.
// All other tools require mcp_admin or higher.
var auditorTools = map[string]bool{
	"ping":                    true,
	"whoami":                  true,
	"list_providers":          true,
	"list_model_aliases":      true,
	"list_model_costs":        true,
	"list_teams":              true,
	"list_api_keys":           true,
	"get_quota":               true,
	"list_quotas":             true,
	"list_request_logs":       true,
	"list_team_model_aliases": true,
	"list_audit_logs":         true,
}

// AuditorToolFilter returns a ToolFilterFunc that restricts tools/list
// to the auditorTools set when the caller holds mcp_auditor scope.
// Higher scopes (mcp_billing, mcp_admin, mcp_super) see all tools.
func AuditorToolFilter() mcpserver.ToolFilterFunc {
	return func(ctx context.Context, tools []mcplib.Tool) []mcplib.Tool {
		ak, ok := authpkg.APIKeyFromContext(ctx)
		if !ok || ak.Scope != "mcp_auditor" {
			return tools // no filtering for non-auditor
		}
		filtered := make([]mcplib.Tool, 0, len(tools))
		for _, t := range tools {
			if auditorTools[t.Name] {
				filtered = append(filtered, t)
			}
		}
		return filtered
	}
}

// RegisterWhoamiTool adds the whoami tool to the server.
// It reports the caller's identity, scope, and tool availability.
func RegisterWhoamiTool(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("whoami",
			mcplib.WithDescription("Show the current API key identity, scope, and available tools.")),
		whoamiHandler(st),
	)
}

func whoamiHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ak, ok := authpkg.APIKeyFromContext(ctx)
		if !ok {
			return mcplib.NewToolResultText(`{"error":"unauthenticated"}`), nil
		}

		result := map[string]any{
			"key_id":  ak.ID,
			"scope":   ak.Scope,
			"team_id": ak.TeamID,
			"label":   ak.CreatedForLabel,
		}

		// Build available/unavailable tool lists based on scope.
		var available, unavailable []string
		for name, minScope := range registry {
			if minScope == "" {
				continue // skip public tools
			}
			callerRank, cOK := scopeRank[ak.Scope]
			minRank, mOK := scopeRank[minScope]
			if !cOK || !mOK {
				continue
			}
			if callerRank >= minRank {
				available = append(available, name)
			} else {
				unavailable = append(unavailable, name)
			}
		}
		sort.Strings(available)
		sort.Strings(unavailable)

		result["available_tools"] = available
		result["unavailable_tools"] = unavailable

		if ak.Scope == "mcp_auditor" && len(unavailable) > 0 {
			result["reason"] = fmt.Sprintf(
				"Your scope (mcp_auditor) allows read-only access. %d tools are restricted to mcp_admin or higher. Contact your super admin to escalate.",
				len(unavailable),
			)
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		return mcplib.NewToolResultText(string(out)), nil
	}
}

// auditDenyDetail returns a structured error message for auditor-scope
// callers attempting a write operation.
func auditDenyDetail(toolName string) string {
	return fmt.Sprintf(
		"permission denied: %s requires mcp_admin or higher, your_scope=mcp_auditor (read-only). Contact your super admin to escalate.",
		toolName,
	)
}

func init() {
	registry["whoami"] = "mcp_auditor"
	registry["list_audit_logs"] = "mcp_auditor"
}
