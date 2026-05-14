package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	lgwmcp "github.com/panda/llm-gateway/internal/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// runMCPServe implements the `mcp-serve` subcommand. It starts an MCP stdio
// server that exposes the gateway's admin API (provider config, model aliases,
// team management, API key issuance) to an AI agent client.
//
// The agent client (Claude Code, Cline, etc.) invokes this binary as a
// subprocess; JSON-RPC messages flow over stdin/stdout. The HTTP gateway
// (`start`) runs as a separate process on port 7421.
//
// Auth: reads LLM_GATEWAY_TOKEN from the environment. Legacy token → mcp_super.
// lgw_-prefixed admin key → bcrypt-verified against the DB, cached 60 s.
func runMCPServe() int {
	cfgDir, err := defaultConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-serve: config dir: %v\n", err)
		return 1
	}
	token, _, err := resolveToken(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-serve: resolve token: %v\n", err)
		return 1
	}

	st, err := openStore(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-serve: open store: %v\n", err)
		return 1
	}
	if st != nil {
		defer st.Close()
	}

	cache := authpkg.NewKeyCache(60 * time.Second)
	s := lgwmcp.Build()
	lgwmcp.RegisterProviderTools(s, st)
	lgwmcp.RegisterTenancyTools(s, st)

	stdio := mcpserver.NewStdioServer(s)
	stdio.SetContextFunc(lgwmcp.MakeContextFunc(token, st, cache))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-serve: %v\n", err)
		return 1
	}
	return 0
}
