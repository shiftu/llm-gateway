package mcp

import (
	"context"
	"errors"
	"fmt"

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

	// provider + alias tools
	"add_provider":         "mcp_admin",
	"remove_provider":      "mcp_admin",
	"list_providers":       "mcp_auditor",
	"set_default_provider": "mcp_admin",
	"set_model_alias":      "mcp_admin",
	"delete_model_alias":   "mcp_admin",
	"list_model_aliases":   "mcp_auditor",
	"set_model_cost":       "mcp_admin",
	"list_model_costs":     "mcp_auditor",

	// tenancy tools
	"add_team":          "mcp_super",
	"list_teams":        "mcp_auditor",
	"issue_api_key":     "mcp_super",
	"revoke_api_key":    "mcp_super",
	"list_api_keys":     "mcp_auditor",
	"set_quota":         "mcp_super",
	"get_quota":         "mcp_auditor",
	"list_quotas":       "mcp_auditor",
	"list_request_logs": "mcp_auditor",
	"get_usage_summary": "mcp_auditor",

	"set_team_model_alias":    "mcp_admin",
	"delete_team_model_alias": "mcp_admin",
	"list_team_model_aliases": "mcp_auditor",

	// Audit tools
	"list_audit_logs": "mcp_auditor",
	"prune_audit_log": "mcp_admin",

	// Identity tool
	"whoami": "mcp_auditor",

	// Health tools (v0.3 T1)
	"get_provider_health": "mcp_auditor",

	// Routing-weights tools (v0.3 T2)
	"set_routing_weights": "mcp_admin",
	"get_routing_weights": "mcp_auditor",

	// Explain trace (v0.3 T5)
	"explain_route_trace": "mcp_auditor",

	// Fallback policy tools (v0.3 T4)
	"set_fallback_policy":    "mcp_admin",
	"remove_fallback_policy": "mcp_admin",
	"list_fallback_policies": "mcp_auditor",

	// Provider capability registry (v0.3 T11)
	"set_provider_capability":    "mcp_admin",
	"list_provider_capabilities": "mcp_auditor",

	// Audit chain verification (v0.3 T22)
	"verify_audit_chain": "mcp_auditor",

	// Per-team budget limits (v0.3 T16)
	"set_budget":   "mcp_admin",
	"check_budget": "mcp_auditor",
	"list_budgets": "mcp_auditor",

	// Master key management (v0.3 T17)
	"rotate_master_key": "mcp_super",
	"rekey_providers":   "mcp_super",
	"retire_master_key": "mcp_super",
	"list_master_keys":  "mcp_super",
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
// When the caller holds mcp_auditor scope and the tool requires higher,
// returns a detailed, actionable error.
func checkTool(ctx context.Context, toolName string) error {
	minScope, ok := registry[toolName]
	if !ok {
		return ErrUnknownTool
	}
	err := requireRole(ctx, minScope)
	if err != nil && errors.Is(err, ErrPermissionDenied) {
		// Produce a more helpful error for auditor-scope callers.
		ak, akOK := authpkg.APIKeyFromContext(ctx)
		if akOK && ak.Scope == "mcp_auditor" {
			return fmt.Errorf(
				"permission denied: %s requires scope=%s, your_scope=mcp_auditor (read-only). Contact your super admin to escalate",
				toolName, minScope,
			)
		}
	}
	return err
}
