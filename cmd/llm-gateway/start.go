package main

import (
	"context"
	"fmt"
	"log"
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
	switch source {
	case tokenSourceEnv:
		log.Print("token source: LLM_GATEWAY_TOKEN env")
	case tokenSourceFile:
		log.Print("token source: ", cfgDir, "/token")
	case tokenSourceEphemeral:
		fmt.Println("No persistent token found — minted an ephemeral one for this session:")
		fmt.Println()
		fmt.Println("  ", token)
		fmt.Println()
		fmt.Println("OpenAI clients:    Authorization: Bearer", token)
		fmt.Println("Anthropic clients: x-api-key:", token)
		fmt.Println()
		fmt.Println("Run `llm-gateway init` to persist a stable token across restarts.")
		fmt.Println()
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
	if st == nil {
		log.Print("store disabled — running in stub mode")
	} else {
		providers, _ := st.ListProviders()
		log.Printf("store ready: %d provider(s) configured", len(providers))
	}

	log.Printf("llm-gateway %s listening on http://%s", version, addr)
	if err := srv.Run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return 1
	}
	log.Print("llm-gateway shut down cleanly")
	return 0
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
