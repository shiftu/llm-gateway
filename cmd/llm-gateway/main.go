package main

import (
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "start":
		return runStart()
	case "version", "-v", "--version":
		fmt.Println("llm-gateway", version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: llm-gateway <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  start    Run the gateway HTTP + MCP server")
	fmt.Fprintln(os.Stderr, "  version  Print version and exit")
	fmt.Fprintln(os.Stderr, "  help     Show this message")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Env vars:")
	fmt.Fprintln(os.Stderr, "  LLM_GATEWAY_TOKEN   Bearer token. If unset, an ephemeral one is minted on start.")
	fmt.Fprintln(os.Stderr, "  LLM_GATEWAY_ADDR    Listen address (default 127.0.0.1:7421)")
}
