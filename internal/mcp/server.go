package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const gatewayVersion = "0.1.0"

// Build constructs and returns a fully-configured MCPServer.
// T7 registers only the ping tool. T8 and T9 call Register* helpers to
// add provider and tenancy tools respectively.
func Build() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"llm-gateway",
		gatewayVersion,
		mcpserver.WithToolCapabilities(false),
	)
	s.AddTool(
		mcplib.NewTool("ping",
			mcplib.WithDescription("Health check — returns gateway version and current UTC time."),
		),
		pingHandler,
	)
	return s
}

// MakeContextFunc returns a StdioContextFunc that verifies LLM_GATEWAY_TOKEN
// from the environment once at server startup and injects an APIKey into the
// shared stdio context. Every tool handler then calls checkTool(ctx, name)
// to enforce ACL without a DB round-trip per call.
//
// Token resolution order:
//  1. Legacy token → synthetic mcp_super key (bootstrap + lost-key recovery).
//  2. lgw_-prefixed key → bcrypt verify against DB, cache result for 60 s.
//  3. Anything else → no key in context (all gated tools will return ErrUnauthenticated).
func MakeContextFunc(legacyToken string, st *store.Store, cache *authpkg.KeyCache) mcpserver.StdioContextFunc {
	return func(ctx context.Context) context.Context {
		token := strings.TrimSpace(os.Getenv("LLM_GATEWAY_TOKEN"))
		if token == "" {
			return ctx
		}
		// Legacy shared secret → grant full admin access.
		if legacyToken != "" && token == legacyToken {
			return authpkg.NewContext(ctx, store.APIKey{
				ID: "__legacy__", Scope: "mcp_super",
			})
		}
		if st == nil || !strings.HasPrefix(token, "lgw_") || len(token) < 20 {
			return ctx
		}
		prefix := token[:20]
		ak, cached := cache.Get(prefix)
		if !cached {
			row, err := st.LookupAPIKeyByPrefix(prefix)
			if err != nil {
				return ctx
			}
			ak = row
		}
		if err := bcrypt.CompareHashAndPassword([]byte(ak.Hash), []byte(token)); err != nil {
			return ctx
		}
		cache.Put(ak)
		return authpkg.NewContext(ctx, ak)
	}
}

// MakeHTTPContextFunc returns an mcp-go HTTPContextFunc for the Streamable
// HTTP transport. Auth for the HTTP path is handled by the upstream
// internal/server middleware chain (Auth.Middleware → legacyKeyShim), which
// has already injected an authpkg.APIKey into r.Context() before mcp-go
// invokes its handler. This function is therefore a thin pass-through whose
// only job is to forward ctx unchanged so the per-request APIKey reaches
// tool handlers via APIKeyFromContext.
//
// Kept as a separate function (not inlined) so tests and future hooks have
// a stable injection point — e.g. attaching a tracing span or per-request
// logger derived from r.Header.
func MakeHTTPContextFunc() mcpserver.HTTPContextFunc {
	return func(ctx context.Context, _ *http.Request) context.Context {
		return ctx
	}
}

func pingHandler(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	out, _ := json.Marshal(map[string]string{
		"gateway": "llm-gateway",
		"version": gatewayVersion,
		"ts":      time.Now().UTC().Format(time.RFC3339),
	})
	return mcplib.NewToolResultText(string(out)), nil
}
