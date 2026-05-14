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
// MCP server.
//
// --client=N selects the target client:
//
//	1 = Claude Code  (~/.claude.json mcpServers block)
//	2 = Claude Desktop (claude_desktop_config.json)
//	3 = Cline (VS Code extension settings)
//	4 = Cursor (.cursor/mcp.json)
//	5 = Generic
//
// --transport={http|stdio} selects the wire transport:
//
//	http  — Streamable HTTP MCP at http://127.0.0.1:7421/mcp (default).
//	        Long-lived process; tool additions roll out via a single
//	        `launchctl kickstart -k lol.jiangtao.llm-gateway` with no
//	        agent restart required.
//	stdio — Legacy stdio subprocess (`llm-gateway mcp-serve`). Kept for
//	        clients that don't yet speak HTTP MCP; new tool additions
//	        require restarting the agent client itself.
func runMCPConfig(args []string) int {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	clientN := fs.String("client", "1", "target client (1=claude-code 2=claude-desktop 3=cline 4=cursor 5=generic)")
	transport := fs.String("transport", "http", "transport: http (default) or stdio")
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

	t := strings.ToLower(*transport)
	if t != "http" && t != "stdio" {
		fmt.Fprintf(os.Stderr, "mcp-config: unknown transport %q; choose http or stdio\n", *transport)
		return 2
	}

	return printMCPConfig(n, t, token)
}

func parseClientN(s string) (int, error) {
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
	fmt.Fprintln(os.Stderr, "  5 = generic         Generic server block")
}

// stdioServerBlock is the JSON shape for legacy stdio MCP clients.
type stdioServerBlock struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// httpServerBlock is the JSON shape for HTTP MCP clients (Claude Code's
// `"type": "http"` form, mirrored by other modern clients).
type httpServerBlock struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func buildStdioBlock(token string) stdioServerBlock {
	return stdioServerBlock{
		Command: "llm-gateway",
		Args:    []string{"mcp-serve"},
		Env:     map[string]string{"LLM_GATEWAY_TOKEN": token},
	}
}

func buildHTTPBlock(token string) httpServerBlock {
	addr := os.Getenv("LLM_GATEWAY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	return httpServerBlock{
		Type:    "http",
		URL:     fmt.Sprintf("http://%s/mcp", addr),
		Headers: map[string]string{"Authorization": "Bearer " + token},
	}
}

func marshalPretty(v any) string {
	b, _ := json.MarshalIndent(v, "  ", "  ")
	return string(b)
}

func printMCPConfig(client int, transport, token string) int {
	var block any
	if transport == "stdio" {
		block = buildStdioBlock(token)
	} else {
		block = buildHTTPBlock(token)
	}
	wrapped := map[string]any{
		"mcpServers": map[string]any{
			"llm-gateway": block,
		},
	}

	switch client {
	case 1: // Claude Code
		fmt.Printf("# Claude Code — add to ~/.claude.json (merge into existing mcpServers) [%s transport]\n", transport)
		fmt.Println("#")
		if transport == "http" {
			fmt.Println("# Tool changes after rebuild only need: launchctl kickstart -k gui/$(id -u)/lol.jiangtao.llm-gateway")
			fmt.Println("# No Claude Code restart required.")
		} else {
			fmt.Println("# Or run: claude mcp add llm-gateway -- llm-gateway mcp-serve")
			fmt.Println("# Note: stdio caches tools at session start — exit and relaunch Claude after rebuilding.")
		}
		fmt.Println()
		fmt.Println("{")
		fmt.Printf("  \"mcpServers\": {\n")
		fmt.Printf("    \"llm-gateway\": %s\n", marshalPretty(block))
		fmt.Printf("  }\n")
		fmt.Println("}")

	case 2: // Claude Desktop
		fmt.Printf("# Claude Desktop — merge into claude_desktop_config.json [%s transport]\n", transport)
		fmt.Println("# macOS:   ~/Library/Application Support/Claude/claude_desktop_config.json")
		fmt.Println("# Windows: %APPDATA%\\Claude\\claude_desktop_config.json")
		fmt.Println()
		b, _ := json.MarshalIndent(wrapped, "", "  ")
		fmt.Println(string(b))

	case 3: // Cline (VS Code)
		fmt.Printf("# Cline (VS Code extension) — open VS Code settings (JSON) and add: [%s transport]\n", transport)
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
		fmt.Printf("# Cursor editor — create or update .cursor/mcp.json in your project root [%s transport]\n", transport)
		fmt.Println("# (or the global ~/.cursor/mcp.json for all projects)")
		fmt.Println()
		b, _ := json.MarshalIndent(wrapped, "", "  ")
		fmt.Println(string(b))

	case 5: // Generic
		fmt.Printf("# Generic MCP server block — paste into your client's mcpServers config [%s transport]\n", transport)
		fmt.Println()
		b, _ := json.MarshalIndent(map[string]any{"llm-gateway": block}, "", "  ")
		fmt.Println(string(b))
	}

	fmt.Println()
	if transport == "http" {
		fmt.Println("# After pasting, restart your client once to pick up the new MCP server.")
		fmt.Println("# The gateway must be running: llm-gateway start  (or via launchd)")
	} else {
		fmt.Println("# After pasting, restart your client to pick up the new MCP server.")
		fmt.Println("# stdio mode spawns a fresh subprocess per session — tool changes require")
		fmt.Println("# exiting and relaunching the client, not just /mcp reconnect.")
	}
	return 0
}
