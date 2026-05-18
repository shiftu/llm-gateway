package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/panda/llm-gateway/internal/encrypt"
	"github.com/panda/llm-gateway/internal/health"
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

	// T17 multi-key: if KEK is set, load active master key from DB.
	// Otherwise fall back to env/file single-key (v0.1 compat).
	kekStr := os.Getenv("LLM_GATEWAY_KEK")
	if kekStr != "" && st != nil {
		kek := encrypt.NewKEKFromString(kekStr)
		if activeMK, err := st.GetActiveMasterKey(kek); err == nil {
			st.SetEncryptor(activeMK)
		} else if errors.Is(err, store.ErrNotFound) {
			// No keys in DB yet — fall through to file/env key
			if mk, mkErr := encrypt.NewFromEnvOrFile(cfgDir); mkErr == nil {
				st.SetEncryptor(mk)
			}
		} else {
			fmt.Fprintf(os.Stderr, "warn: could not load active master key from DB: %v\n", err)
		}
	} else {
		mk, mkErr := encrypt.NewFromEnvOrFile(cfgDir)
		if mkErr == nil {
			st.SetEncryptor(mk)
		} else {
			fmt.Fprintf(os.Stderr, "note: no master key — provider API keys stored plaintext\n")
		}
	}
	if err := seedProviderFromEnv(st); err != nil {
		fmt.Fprintf(os.Stderr, "could not seed provider from env: %v\n", err)
		return 1
	}
	if err := seedModelCosts(st); err != nil {
		fmt.Fprintf(os.Stderr, "could not seed model costs: %v\n", err)
		return 1
	}
	srv := server.NewServer(token, st)

	// v0.3 T1 Q11 startup gate: with LLM_GATEWAY_HEALTH_PROBES=1, probe every
	// configured provider once (10s deadline) before flipping /healthz to 200.
	// Then start the background loop. With the env unset, MarkReady fires
	// immediately and the get_provider_health MCP tool reports
	// "probes disabled". Health probes are opt-in for v0.3.0.
	var mgr *health.Manager
	if os.Getenv("LLM_GATEWAY_HEALTH_PROBES") == "1" && st != nil {
		mgr = health.NewManager(health.Probe, 30*time.Second)
		if n, err := server.RegisterHealthTargets(mgr, st); err != nil {
			fmt.Fprintf(os.Stderr, "warn: could not register health targets: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "health: probing %d provider(s) at startup...\n", n)
			bootCtx, bootCancel := context.WithTimeout(ctx, 10*time.Second)
			_ = mgr.Bootstrap(bootCtx)
			bootCancel()
			go mgr.Run(ctx)
		}
	}
	srv.MountMCP(server.BuildMCPHandler(st, mgr))

	printBanner(token, source, cfgDir, addr, st)
	srv.MarkReady()

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
	fmt.Printf(" MCP HTTP:  http://%s/mcp  (Bearer %s)\n", addr, tok8)
	fmt.Println(" MCP cfg:   llm-gateway mcp-config --client=1")
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

// seedModelCosts upserts published pricing for DeepSeek and GLM models so that
// cost tracking works out-of-the-box. Uses SetModelCost's ON CONFLICT DO UPDATE
// so repeated starts are idempotent. Prices reflect 2025-05 rate cards:
//
//	DeepSeek V4 Flash (R1-based reasoning): $0.55/$2.19 per M tokens in/out
//	DeepSeek V4 Pro  (V3 chat):             $0.27/$1.10 per M tokens in/out
//	GLM-4-Plus       (Zhipu):               ¥0.05/1k ≈ $0.007/1k in/out
func seedModelCosts(st *store.Store) error {
	if st == nil {
		return nil
	}
	eff := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	r219 := 0.00219 // deepseek-v4-flash reasoning token price (same as output)

	costs := []store.ModelCost{
		{
			Provider: "deepseek", Model: "deepseek-v4-flash",
			USDPerInput1k: 0.00055, USDPerOutput1k: 0.00219,
			USDPerReasoning1k: &r219, EffectiveFrom: eff,
		},
		{
			Provider: "deepseek", Model: "deepseek-v4-pro",
			USDPerInput1k: 0.00027, USDPerOutput1k: 0.00110,
			EffectiveFrom: eff,
		},
		{
			Provider: "glm-prod", Model: "glm-4-plus",
			USDPerInput1k: 0.007, USDPerOutput1k: 0.007,
			EffectiveFrom: eff,
		},
	}
	for _, c := range costs {
		if err := st.SetModelCost(c); err != nil {
			return err
		}
	}
	return nil
}
