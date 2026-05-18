package mcp

import (
	"context"
	"encoding/json"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterAuditTools adds the verify_audit_chain tool (v0.3 T22).
func RegisterAuditTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("verify_audit_chain",
			mcplib.WithDescription("Verify the admin_audit hash chain for tamper-evidence. Returns ok=true if all hashed entries are intact."),
		),
		verifyAuditChainHandler(st),
	)
}

func verifyAuditChainHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "verify_audit_chain"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		result, err := st.VerifyAuditChain()
		if err != nil {
			return mcplib.NewToolResultError("verify_audit_chain failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(result)
		return mcplib.NewToolResultText(string(out)), nil
	}
}
