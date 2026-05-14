package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// runMCPConfig implements the `mcp-config` subcommand. Prints the JSON config
// snippet the user must paste into their AI client to wire up llm-gateway's
// MCP stdio server.
//
// --client=N selects the target client:
//
//	1 = Claude Code  (~/.claude.json mcpServers block)
//	2 = Claude Desktop (claude_desktop_config.json)
//	3 = Cline (VS Code extension settings)
//	4 = Cursor (.cursor/mcp.json)
//	5 = Generic (raw stdio server JSON, works anywhere)
func runMCPConfig(args []string) int {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	clientN := fs.String("client", "1", "target client (1=claude-code 2=claude-desktop 3=cline 4=cursor 5=generic)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfgDir, err := defaultConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-config: config dir: %v\n", err)
		return 1
	}
	token, _, err := resolveToken(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-config: resolve token: %v\n", err)
		return 1
	}

	n, err := parseClientN(*clientN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-config: %v\n", err)
		printClientList()
		return 2
	}

	return printMCPConfig(n, token)
}

func parseClientN(s string) (int, error) {
	// Accept both numeric ("1") and named ("claude-code") forms.
	named := map[string]int{
		"claude-code":    1,
		"claude-desktop": 2,
		"cline":          3,
		"cursor":         4,
		"generic":        5,
	}
	if v, ok := named[strings.ToLower(s)]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 5 {
		return 0, fmt.Errorf("unknown client %q; choose 1–5 or a name (see --help)", s)
	}
	return n, nil
}

func printClientList() {
	fmt.Fprintln(os.Stderr, "  1 = claude-code    Claude Code (~/.claude.json)")
	fmt.Fprintln(os.Stderr, "  2 = claude-desktop  Claude Desktop (claude_desktop_config.json)")
	fmt.Fprintln(os.Stderr, "  3 = cline           Cline VS Code extension")
	fmt.Fprintln(os.Stderr, "  4 = cursor          Cursor editor (.cursor/mcp.json)")
	fmt.Fprintln(os.Stderr, "  5 = generic         Generic stdio server block")
}

// mcpServerBlock is the common JSON shape for all stdio MCP clients.
type mcpServerBlock struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func buildServerBlock(token string) mcpServerBlock {
	return mcpServerBlock{
		Command: "llm-gateway",
		Args:    []string{"mcp-serve"},
		Env:     map[string]string{"LLM_GATEWAY_TOKEN": token},
	}
}

func marshalPretty(v any) string {
	b, _ := json.MarshalIndent(v, "  ", "  ")
	return string(b)
}

func printMCPConfig(client int, token string) int {
	block := buildServerBlock(token)
	wrapped := map[string]any{
		"mcpServers": map[string]any{
			"llm-gateway": block,
		},
	}

	switch client {
	case 1: // Claude Code
		fmt.Println("# Claude Code — add to ~/.claude.json (merge into existing mcpServers)")
		fmt.Println("#")
		fmt.Println("# Or run: claude mcp add llm-gateway -- llm-gateway mcp-serve")
		fmt.Println()
		fmt.Println("{")
		fmt.Printf("  \"mcpServers\": {\n")
		fmt.Printf("    \"llm-gateway\": %s\n", marshalPretty(block))
		fmt.Printf("  }\n")
		fmt.Println("}")

	case 2: // Claude Desktop
		fmt.Println("# Claude Desktop — merge into claude_desktop_config.json")
		fmt.Println("# macOS:   ~/Library/Application Support/Claude/claude_desktop_config.json")
		fmt.Println("# Windows: %APPDATA%\\Claude\\claude_desktop_config.json")
		fmt.Println()
		b, _ := json.MarshalIndent(wrapped, "", "  ")
		fmt.Println(string(b))

	case 3: // Cline (VS Code)
		fmt.Println("# Cline (VS Code extension) — open VS Code settings (JSON) and add:")
		fmt.Println("# Preferences: Open User Settings (JSON)  →  add inside the root object:")
		fmt.Println()
		clineBlock := map[string]any{
			"cline.mcpServers": map[string]any{
				"llm-gateway": block,
			},
		}
		b, _ := json.MarshalIndent(clineBlock, "", "  ")
		fmt.Println(string(b))

	case 4: // Cursor
		fmt.Println("# Cursor editor — create or update .cursor/mcp.json in your project root")
		fmt.Println("# (or the global ~/.cursor/mcp.json for all projects)")
		fmt.Println()
		b, _ := json.MarshalIndent(wrapped, "", "  ")
		fmt.Println(string(b))

	case 5: // Generic
		fmt.Println("# Generic MCP stdio server block — paste into your client's mcpServers config")
		fmt.Println()
		b, _ := json.MarshalIndent(map[string]any{"llm-gateway": block}, "", "  ")
		fmt.Println(string(b))
	}

	fmt.Println()
	fmt.Println("# After pasting, restart your client to pick up the new MCP server.")
	fmt.Println("# The gateway must be running: llm-gateway start")
	return 0
}
