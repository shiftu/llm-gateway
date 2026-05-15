package mcp

// acl_test.go covers T7: declarative ACL registry + requireRole.
// Tests run inside the package to access unexported symbols.

import (
	"context"
	"errors"
	"testing"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func ctxWithScope(scope string) context.Context {
	return authpkg.NewContext(context.Background(), store.APIKey{
		ID: "ak_test", Scope: scope,
	})
}

func TestRequireRole_PublicTool_NoAuth_Passes(t *testing.T) {
	if err := requireRole(context.Background(), ""); err != nil {
		t.Errorf("public tool should always pass, got %v", err)
	}
}

func TestRequireRole_PublicTool_AnyScope_Passes(t *testing.T) {
	for _, scope := range []string{"inbound", "mcp_admin", "mcp_super"} {
		if err := requireRole(ctxWithScope(scope), ""); err != nil {
			t.Errorf("scope %q on public tool should pass, got %v", scope, err)
		}
	}
}

func TestRequireRole_NoKey_PrivateTool_Unauthenticated(t *testing.T) {
	err := requireRole(context.Background(), "mcp_admin")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("want ErrUnauthenticated, got %v", err)
	}
}

func TestRequireRole_Inbound_Denied_AdminTool(t *testing.T) {
	err := requireRole(ctxWithScope("inbound"), "mcp_admin")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("inbound scope on mcp_admin tool should be denied, got %v", err)
	}
}

func TestRequireRole_Admin_Passes_AdminTool(t *testing.T) {
	if err := requireRole(ctxWithScope("mcp_admin"), "mcp_admin"); err != nil {
		t.Errorf("mcp_admin should pass mcp_admin requirement, got %v", err)
	}
}

func TestRequireRole_Super_Passes_AdminTool(t *testing.T) {
	// mcp_super rank (4) >= mcp_admin rank (3)
	if err := requireRole(ctxWithScope("mcp_super"), "mcp_admin"); err != nil {
		t.Errorf("mcp_super should pass mcp_admin requirement, got %v", err)
	}
}

func TestRequireRole_Admin_Denied_SuperTool(t *testing.T) {
	err := requireRole(ctxWithScope("mcp_admin"), "mcp_super")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("mcp_admin should be denied mcp_super tool, got %v", err)
	}
}

func TestRequireRole_Auditor_Denied_AdminTool(t *testing.T) {
	err := requireRole(ctxWithScope("mcp_auditor"), "mcp_admin")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("mcp_auditor should be denied mcp_admin tool, got %v", err)
	}
}

func TestCheckTool_UnknownTool_ReturnsError(t *testing.T) {
	err := checkTool(context.Background(), "nonexistent_tool_xyz")
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("want ErrUnknownTool, got %v", err)
	}
}

func TestCheckTool_Ping_NoAuth_Passes(t *testing.T) {
	if err := checkTool(context.Background(), "ping"); err != nil {
		t.Errorf("ping should be public, got %v", err)
	}
}

func TestCheckTool_AddTeam_RequiresSuper(t *testing.T) {
	// mcp_admin cannot add a team
	err := checkTool(ctxWithScope("mcp_admin"), "add_team")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("add_team requires mcp_super, mcp_admin should be denied, got %v", err)
	}
	// mcp_super can
	if err := checkTool(ctxWithScope("mcp_super"), "add_team"); err != nil {
		t.Errorf("mcp_super should pass add_team, got %v", err)
	}
}

func TestCheckTool_ListProviders_RequiresAdmin(t *testing.T) {
	if err := checkTool(ctxWithScope("mcp_admin"), "list_providers"); err != nil {
		t.Errorf("mcp_admin should pass list_providers, got %v", err)
	}
	err := checkTool(ctxWithScope("inbound"), "list_providers")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("inbound should be denied list_providers, got %v", err)
	}
}

func TestBuild_Compiles_And_HasPing(t *testing.T) {
	s := Build()
	if s == nil {
		t.Fatal("Build() returned nil")
	}
}

// --- T7: auditor scope read-only filter ---

func TestCheckTool_Auditor_GetsDetailedDeniedMessage(t *testing.T) {
	// Auditor calling a write tool should get a message with scope details.
	err := checkTool(ctxWithScope("mcp_auditor"), "add_provider")
	if err == nil {
		t.Fatal("auditor should be denied add_provider")
	}
	errMsg := err.Error()
	if !contains(errMsg, "your_scope=mcp_auditor") {
		t.Errorf("error should mention auditor scope, got: %s", errMsg)
	}
	if !contains(errMsg, "mcp_admin") {
		t.Errorf("error should mention required scope mcp_admin, got: %s", errMsg)
	}
}

func TestCheckTool_Auditor_ReadTools_Pass(t *testing.T) {
	readTools := []string{
		"list_providers", "list_model_aliases", "list_model_costs",
		"list_teams", "list_api_keys",
		"get_quota", "list_quotas", "list_request_logs",
		"list_team_model_aliases", "list_audit_logs",
		"whoami",
	}
	for _, tool := range readTools {
		if err := checkTool(ctxWithScope("mcp_auditor"), tool); err != nil {
			t.Errorf("auditor should pass %s, got %v", tool, err)
		}
	}
}

func TestCheckTool_Auditor_WriteTools_Denied(t *testing.T) {
	writeTools := []string{
		"add_provider", "remove_provider", "set_default_provider",
		"set_model_alias", "delete_model_alias",
		"set_model_cost",
		"add_team", "issue_api_key", "revoke_api_key",
		"set_quota",
		"set_team_model_alias", "delete_team_model_alias",
		"prune_audit_log",
	}
	for _, tool := range writeTools {
		err := checkTool(ctxWithScope("mcp_auditor"), tool)
		if err == nil {
			t.Errorf("auditor should be denied %s", tool)
		}
	}
}

func TestAuditorToolFilter_AuditorScope_OnlySeesReadOnly(t *testing.T) {
	filter := AuditorToolFilter()
	allTools := []mcplib.Tool{
		{Name: "ping"},
		{Name: "whoami"},
		{Name: "list_teams"},
		{Name: "add_provider"},
		{Name: "set_quota"},
		{Name: "list_audit_logs"},
	}

	// Non-auditor sees all
	nonAuditorCtx := ctxWithScope("mcp_admin")
	filtered := filter(nonAuditorCtx, allTools)
	if len(filtered) != len(allTools) {
		t.Errorf("non-auditor should see all %d tools, got %d", len(allTools), len(filtered))
	}

	// Auditor sees only read-only
	auditorCtx := ctxWithScope("mcp_auditor")
	filtered = filter(auditorCtx, allTools)
	if len(filtered) != 4 {
		t.Errorf("auditor should see 4 read-only tools, got %d: %v", len(filtered), toolNames(filtered))
	}
}

func toolNames(tools []mcplib.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
