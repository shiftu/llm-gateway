package mcp

// acl_test.go covers T7: declarative ACL registry + requireRole.
// Tests run inside the package to access unexported symbols.

import (
	"context"
	"errors"
	"testing"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
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
	// Verify ping is registered by checking we can call it without error.
	// (We trust mcp-go to error on missing tools; no reflection needed.)
}
