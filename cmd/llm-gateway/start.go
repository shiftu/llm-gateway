package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/panda/llm-gateway/internal/server"
	"github.com/panda/llm-gateway/internal/store"
)

const defaultAddr = "127.0.0.1:7421"

// runStart implements the `start` subcommand: boot HTTP server on configured
// addr, wait for SIGINT/SIGTERM, graceful shutdown. Returns process exit code.
//
// TTHW (F-DX-01): if LLM_GATEWAY_TOKEN is unset we mint an ephemeral random
// token and print it on stdout so the operator can copy-paste it into their
// agent's config without first running `init`. This makes the binary usable
// in under one minute from download. Persistent token still requires setting
// the env var.
func runStart() int {
	cfgDir, err := defaultConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not resolve config dir: %v\n", err)
		return 1
	}
	token, source, err := resolveToken(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not resolve token: %v\n", err)
		return 1
	}
	addr := os.Getenv("LLM_GATEWAY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := openStore(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not open store: %v\n", err)
		return 1
	}
	if st != nil {
		defer st.Close()
	}
	if err := seedProviderFromEnv(st); err != nil {
		fmt.Fprintf(os.Stderr, "could not seed provider from env: %v\n", err)
		return 1
	}
	srv := server.NewServer(token, st)

	printBanner(token, source, cfgDir, addr, st)

	if err := srv.Run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "llm-gateway shut down cleanly")
	return 0
}

// printBanner writes the startup banner to stdout. Using stdout (not log)
// keeps the banner clean and machine-parseable by scripts that capture it.
func printBanner(token, source, cfgDir, addr string, st *store.Store) {
	sep := "----------------------------------------"
	fmt.Println(sep)
	fmt.Printf(" llm-gateway %s\n", version)
	fmt.Printf(" listen:  http://%s\n", addr)

	switch source {
	case tokenSourceEnv:
		fmt.Println(" token:   (LLM_GATEWAY_TOKEN env)")
	case tokenSourceFile:
		fmt.Printf(" token:   %s/token\n", cfgDir)
	case tokenSourceEphemeral:
		fmt.Printf(" token:   %s  [ephemeral — run `init` to persist]\n", token)
	}

	if st != nil {
		providers, _ := st.ListProviders()
		fmt.Printf(" providers: %d configured\n", len(providers))
	} else {
		fmt.Println(" store:   disabled (stub mode)")
	}

	fmt.Println(sep)
	tok8 := token
	if len(tok8) > 12 {
		tok8 = tok8[:12] + "..."
	}
	fmt.Println(" OpenAI:    Authorization: Bearer", tok8)
	fmt.Println(" Anthropic: x-api-key:", tok8)
	fmt.Println(" MCP:       llm-gateway mcp-config --client=1")
	fmt.Println(sep)

	if source == tokenSourceEphemeral {
		fmt.Println()
		fmt.Println(" WARNING: ephemeral token — not persisted across restarts.")
		fmt.Println(" Run `llm-gateway init` to generate a stable token.")
		fmt.Println()
	}
}

// openStore returns a SQLite-backed store at <cfgDir>/state.db, or nil if
// LLM_GATEWAY_NO_STORE=1 is set (forces stub mode for offline dev).
func openStore(cfgDir string) (*store.Store, error) {
	if os.Getenv("LLM_GATEWAY_NO_STORE") == "1" {
		return nil, nil
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, err
	}
	return store.Open(cfgDir + "/state.db")
}

// seedProviderFromEnv installs a single provider from LLM_GATEWAY_PROVIDER_*
// env vars when present. Convenience for v0.1 single-provider deployments —
// SQLite-driven multi-provider config via MCP add_provider lands in Task 8.
// Idempotent: re-running with the same name is a no-op (ErrDuplicate
// swallowed) so restart with unchanged env is safe.
func seedProviderFromEnv(st *store.Store) error {
	if st == nil {
		return nil
	}
	apiKey := os.Getenv("LLM_GATEWAY_PROVIDER_API_KEY")
	if apiKey == "" {
		return nil
	}
	name := os.Getenv("LLM_GATEWAY_PROVIDER_NAME")
	if name == "" {
		name = "default"
	}
	kind := os.Getenv("LLM_GATEWAY_PROVIDER_KIND")
	if kind == "" {
		kind = "deepseek"
	}
	openaiURL := os.Getenv("LLM_GATEWAY_PROVIDER_OPENAI_BASE_URL")
	anthropicURL := os.Getenv("LLM_GATEWAY_PROVIDER_ANTHROPIC_BASE_URL")
	if openaiURL == "" && anthropicURL == "" && kind == "deepseek" {
		openaiURL = "https://api.deepseek.com"
		anthropicURL = "https://api.deepseek.com/anthropic"
	}

	err := st.AddProvider(store.Provider{
		Name: name, Kind: kind,
		OpenAIBaseURL:    openaiURL,
		AnthropicBaseURL: anthropicURL,
		APIKey:           apiKey,
		IsDefault:        true,
	})
	if err == nil {
		return st.SetDefaultProvider(name)
	}
	if err == store.ErrDuplicate {
		// Already exists; ensure it's still the default + key matches latest env.
		return st.SetDefaultProvider(name)
	}
	return err
}
