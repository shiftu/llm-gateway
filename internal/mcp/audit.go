package mcp

import (
	"github.com/panda/llm-gateway/internal/store"
)

// audit logs a mutating admin action. Called at the CONCLUSION of every
// mutating MCP tool (after success). Errors are silently ignored — audit
// logging must never block or fail the user's operation.
func audit(st *store.Store, action, targetType, targetID string, detail any) {
	_ = st.LogAdminAction(action, targetType, targetID, detail)
}
