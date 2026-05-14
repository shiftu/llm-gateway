package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/panda/llm-gateway/internal/store"
)

// Token source-of-truth labels returned by resolveToken so callers can log
// or test which path won.
const (
	tokenSourceEnv       = "env"
	tokenSourceFile      = "file"
	tokenSourceEphemeral = "ephemeral"
)

// ErrAlreadyInitialized signals that an init was attempted against a config
// directory that already has a token file. The operator must pass --force to
// overwrite, matching plan F-6 idempotency requirement.
var ErrAlreadyInitialized = errors.New("llm-gateway already initialized; re-run with --force to overwrite")

// defaultConfigDir is ~/.config/llm-gateway. Honours XDG_CONFIG_HOME if set.
func defaultConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "llm-gateway"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "llm-gateway"), nil
}

// initializeTokenFile creates dir if absent and writes a fresh 24-byte random
// token to dir/token with 0600 perms. Returns ErrAlreadyInitialized if the
// file exists and force is false.
func initializeTokenFile(dir string, force bool) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "token")
	if _, err := os.Stat(path); err == nil && !force {
		return "", ErrAlreadyInitialized
	}
	tok, err := mintToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// resolveToken returns the bearer token for `start` to use, plus a label
// indicating which source provided it. Priority: env > file > ephemeral.
// Ephemeral tokens are NOT written to disk so that running `init` remains
// the one obvious way to get a stable token.
func resolveToken(dir string) (token, source string, err error) {
	if v := os.Getenv("LLM_GATEWAY_TOKEN"); v != "" {
		return v, tokenSourceEnv, nil
	}
	path := filepath.Join(dir, "token")
	if data, readErr := os.ReadFile(path); readErr == nil {
		tok := string(data)
		if tok == "" {
			return "", "", fmt.Errorf("token file %s is empty; re-run `llm-gateway init --force`", path)
		}
		return tok, tokenSourceFile, nil
	}
	tok, err := mintToken()
	if err != nil {
		return "", "", err
	}
	return tok, tokenSourceEphemeral, nil
}

func mintToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "lgw_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// runInit implements the `init` subcommand. Writes the token file, then
// prints copy-pastable config snippets for Cline and Claude Desktop so the
// operator can wire the gateway into their agent in one minute (F-DX-01).
//
// --team=<slug> additionally creates a team in the store and issues an
// inbound-scoped API key for it, printing the lgw_ token once at issuance.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing token file")
	addr := fs.String("addr", defaultAddr, "gateway listen address (for printed config snippets)")
	team := fs.String("team", "", "create a team with this slug and issue an inbound API key")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := defaultConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not resolve config dir: %v\n", err)
		return 1
	}

	tok, err := initializeTokenFile(dir, *force)
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
		return 1
	}

	tokenPath := filepath.Join(dir, "token")
	fmt.Println("llm-gateway initialized")
	fmt.Println()
	fmt.Println("Token written to:", tokenPath, "(0600)")
	fmt.Println("Token value:    ", tok)
	fmt.Println()
	fmt.Println("Start the gateway:")
	fmt.Println("  llm-gateway start")
	fmt.Println()
	fmt.Println("OpenAI-compat clients (Cline / Cursor / Continue / SDKs):")
	fmt.Println("  base_url:      http://" + *addr + "/v1")
	fmt.Println("  Authorization: Bearer " + tok)
	fmt.Println()
	fmt.Println("Anthropic-compat clients (Claude Code / Claude Desktop):")
	fmt.Println("  base_url:      http://" + *addr)
	fmt.Println("  x-api-key:     " + tok)
	fmt.Println()
	fmt.Println("Configure an upstream LLM provider via env on `start`:")
	fmt.Println("  LLM_GATEWAY_PROVIDER_KIND=deepseek \\")
	fmt.Println("  LLM_GATEWAY_PROVIDER_API_KEY=<your-key> \\")
	fmt.Println("  llm-gateway start")

	if *team != "" {
		if code := runInitTeam(dir, *team); code != 0 {
			return code
		}
	}
	return 0
}

// runInitTeam opens (or creates) the store, ensures the team exists, issues an
// inbound API key, and prints the token. Idempotent: if the team already exists
// a new key is still issued (multiple keys per team are fine).
func runInitTeam(cfgDir, slug string) int {
	st, err := openStore(cfgDir)
	if err != nil || st == nil {
		fmt.Fprintf(os.Stderr, "init --team: could not open store: %v\n", err)
		return 1
	}
	defer st.Close()

	team, err := st.AddTeam(slug, slug)
	if errors.Is(err, store.ErrDuplicate) {
		team, err = st.GetTeamBySlug(slug)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "init --team: team %q: %v\n", slug, err)
		return 1
	}

	_, apiTok, err := st.IssueAPIKey(team.ID, "inbound", "init")
	if err != nil {
		fmt.Fprintf(os.Stderr, "init --team: issue API key: %v\n", err)
		return 1
	}

	fmt.Println()
	fmt.Println("Team:           ", slug)
	fmt.Println("Inbound API key (shown once):")
	fmt.Println("  " + apiTok)
	fmt.Println()
	fmt.Println("Use this key instead of the legacy token for inbound requests:")
	fmt.Println("  Authorization: Bearer " + apiTok)
	fmt.Println("  x-api-key:     " + apiTok)
	return 0
}
