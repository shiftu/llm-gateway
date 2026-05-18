package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// runHealth implements the `health` subcommand: makes an HTTP GET to the
// /healthz endpoint on the configured addr and exits 0 on 200, non-zero on
// failure. Designed for Docker HEALTHCHECK on a scratch image.
func runHealth(args []string) int {
	addr := os.Getenv("LLM_GATEWAY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	// Docker HEALTHCHECK connects via the container's network, so 127.0.0.1
	// must match the server's bind address.
	url := fmt.Sprintf("http://%s/healthz", addr)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check got HTTP %d\n", resp.StatusCode)
		return 1
	}
	fmt.Fprintln(os.Stderr, "health check OK")
	return 0
}
