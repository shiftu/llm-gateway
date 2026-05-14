package mcp

import (
	"context"
	"errors"

	authpkg "github.com/panda/llm-gateway/internal/auth"
)

// scopeRank assigns a numeric rank to each RBAC scope.
// Higher rank = more privileged. Callers must have rank >= the required rank.
var scopeRank = map[string]int{
	"inbound":     0,
	"mcp_auditor": 1,
	"mcp_billing": 2,
	"mcp_admin":   3,
	"mcp_super":   4,
}

// registry maps every MCP tool name to its minimum required scope.
// Empty string means the tool is public (no auth needed).
// This is the single source of truth for ACL enforcement; every tool
// registered in Build or Register* must have an entry here.
var registry = map[string]string{
	"ping": "",

	// T8: provider + alias tools
	"add_provider":         "mcp_admin",
	"remove_provider":      "mcp_admin",
	"list_providers":       "mcp_admin",
	"set_default_provider": "mcp_admin",
	"set_model_alias":      "mcp_admin",
	"delete_model_alias":   "mcp_admin",
	"list_model_aliases":   "mcp_admin",
	"set_model_cost":       "mcp_admin",
	"list_model_costs":     "mcp_admin",

	// T9: tenancy tools
	"add_team":        "mcp_super",
	"list_teams":      "mcp_admin",
	"issue_api_key":   "mcp_super",
	"revoke_api_key":  "mcp_super",
	"list_api_keys":   "mcp_admin",
	"set_quota":       "mcp_super",
	"get_quota":       "mcp_admin",
	"list_quotas":     "mcp_admin",
	"list_request_logs": "mcp_admin",

	"set_team_model_alias":    "mcp_admin",
	"delete_team_model_alias": "mcp_admin",
	"list_team_model_aliases": "mcp_admin",
}

var (
	ErrUnknownTool      = errors.New("mcp: unknown tool")
	ErrPermissionDenied = errors.New("mcp: permission denied")
	ErrUnauthenticated  = errors.New("mcp: unauthenticated")
)

// requireRole checks that the caller's scope in ctx meets or exceeds minScope.
// Empty minScope → public tool, always passes.
// No APIKey in ctx → ErrUnauthenticated (unless minScope is empty).
func requireRole(ctx context.Context, minScope string) error {
	if minScope == "" {
		return nil
	}
	ak, ok := authpkg.APIKeyFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	callerRank, callerKnown := scopeRank[ak.Scope]
	minRank, minKnown := scopeRank[minScope]
	if !callerKnown || !minKnown {
		return ErrPermissionDenied
	}
	if callerRank < minRank {
		return ErrPermissionDenied
	}
	return nil
}

// checkTool looks up toolName in the registry and delegates to requireRole.
// Returns ErrUnknownTool for tools with no registry entry.
func checkTool(ctx context.Context, toolName string) error {
	minScope, ok := registry[toolName]
	if !ok {
		return ErrUnknownTool
	}
	return requireRole(ctx, minScope)
}
