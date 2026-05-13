package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/panda/llm-gateway/internal/provider"
	"github.com/panda/llm-gateway/internal/server"
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

	prov := loadProviderFromEnv()
	srv := server.NewServer(token, prov)
	if prov != nil {
		log.Printf("upstream provider: %s (kind=%s)", prov.Name, prov.Kind)
	} else {
		log.Print("no provider configured — running in stub mode (set LLM_GATEWAY_PROVIDER_API_KEY to enable real upstream)")
	}

	log.Printf("llm-gateway %s listening on http://%s", version, addr)
	if err := srv.Run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return 1
	}
	log.Print("llm-gateway shut down cleanly")
	return 0
}

// loadProviderFromEnv reads a single optional upstream provider config from
// env. v0.1 transient — SQLite-driven multi-provider lands in Task 4/5.
// Returns nil when LLM_GATEWAY_PROVIDER_API_KEY is unset (stub mode).
func loadProviderFromEnv() *provider.Provider {
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
		kind = "deepseek" // single-provider v0.1 default; matches our spike
	}
	openaiURL := os.Getenv("LLM_GATEWAY_PROVIDER_OPENAI_BASE_URL")
	anthropicURL := os.Getenv("LLM_GATEWAY_PROVIDER_ANTHROPIC_BASE_URL")
	if openaiURL == "" && anthropicURL == "" && kind == "deepseek" {
		openaiURL = "https://api.deepseek.com"
		anthropicURL = "https://api.deepseek.com/anthropic"
	}
	return &provider.Provider{
		Name:             name,
		Kind:             kind,
		OpenAIBaseURL:    openaiURL,
		AnthropicBaseURL: anthropicURL,
		APIKey:           apiKey,
	}
}
