package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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
	token := os.Getenv("LLM_GATEWAY_TOKEN")
	if token == "" {
		t, err := generateEphemeralToken()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to mint ephemeral token: %v\n", err)
			return 1
		}
		token = t
		fmt.Println("LLM_GATEWAY_TOKEN not set — minted an ephemeral token for this session:")
		fmt.Println()
		fmt.Println("  ", token)
		fmt.Println()
		fmt.Println("OpenAI clients:    Authorization: Bearer", token)
		fmt.Println("Anthropic clients: x-api-key:", token)
		fmt.Println()
		fmt.Println("Set LLM_GATEWAY_TOKEN env to persist a stable token across restarts.")
		fmt.Println()
	}

	addr := os.Getenv("LLM_GATEWAY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv := server.NewServer(token)

	log.Printf("llm-gateway %s listening on http://%s", version, addr)
	if err := srv.Run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return 1
	}
	log.Print("llm-gateway shut down cleanly")
	return 0
}

func generateEphemeralToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "lgw_ephem_" + base64.RawURLEncoding.EncodeToString(b), nil
}
