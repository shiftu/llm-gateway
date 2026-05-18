package server

import (
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/health"
	lgwmcp "github.com/panda/llm-gateway/internal/mcp"
	"github.com/panda/llm-gateway/internal/store"
)

// BuildMCPHandler constructs the /mcp request handler: a legacy-key shim
// wrapping mcp-go's Streamable HTTP transport.
//
// The handler does NOT include its own Auth.Middleware. /mcp is registered on
// the same mux as /v1/* and goes through Server.Handler()'s outer auth chain,
// which already authenticates the caller. legacyKeyShim runs AFTER outer auth
// to backfill an authpkg.APIKey for legacy-token callers (the auth middleware
// only injects one on the lgw_ path); this mirrors the synthetic __legacy__
// key the stdio transport's MakeContextFunc creates, so MCP tool ACL checks
// see the same caller identity regardless of transport.
//
// Quota middleware skips /mcp by path — see quota.go — so admin MCP traffic
// doesn't burn an lgw_ key's inbound RPM/day budget.
func BuildMCPHandler(st *store.Store, mgr *health.Manager) http.Handler {
	s := lgwmcp.Build()
	lgwmcp.RegisterProviderTools(s, st)
	lgwmcp.RegisterTenancyTools(s, st)
	lgwmcp.RegisterRoutingTools(s, st)
	lgwmcp.RegisterWhoamiTool(s, st)
	lgwmcp.RegisterHealthTools(s, mgr)
	lgwmcp.RegisterRoutingWeightTools(s, st)
	lgwmcp.RegisterExplainTools(s, st)
	lgwmcp.RegisterFallbackTools(s, st)
	lgwmcp.RegisterCapabilityTools(s, st)
	lgwmcp.RegisterResources(s, st)
	lgwmcp.RegisterPrompts(s, st)

	// Stateless mode: every POST is a fresh session, so callers (curl,
	// Claude Code, Hermes, etc.) skip the initialize → Mcp-Session-Id
	// handshake. Matches the admin-endpoint pattern — tools are
	// stateless RPCs, the gateway already persists everything to SQLite.
	streamable := mcpserver.NewStreamableHTTPServer(s,
		mcpserver.WithStateLess(true),
		mcpserver.WithHTTPContextFunc(lgwmcp.MakeHTTPContextFunc()),
	)
	return legacyKeyShim(streamable)
}

// legacyKeyShim ensures every authenticated /mcp request has an APIKey in
// context. Auth.Middleware injects one on the lgw_ path but not on the legacy
// shared-token path (where it just calls next.ServeHTTP). For MCP that
// difference matters because tool ACL reads APIKeyFromContext. This shim
// synthesises an mcp_super key when ctx lacks one — mirroring the synthetic
// __legacy__ key produced by internal/mcp/server.go:MakeContextFunc for the
// stdio transport, so both transports treat the legacy token identically.
func legacyKeyShim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authpkg.APIKeyFromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		synthetic := store.APIKey{ID: "__legacy__", Scope: "mcp_super"}
		next.ServeHTTP(w, r.WithContext(authpkg.NewContext(r.Context(), synthetic)))
	})
}
