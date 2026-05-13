package main

import (
	"os"
	"path/filepath"
	"testing"
)

// init_test.go covers Task 11: persistent token via `llm-gateway init`.
//
// Token resolution order (start.go):
//   1. env LLM_GATEWAY_TOKEN — explicit override, dev convenience
//   2. file <cfg>/token (0600) — `init` persists here
//   3. ephemeral random — auto-minted when neither exists, prints to stdout
//
// `init` writes the file with 0600 perms; refuses to clobber an existing
// file unless --force (F-6 idempotency rule).

func TestInit_FreshDir_WritesTokenFile_0600(t *testing.T) {
	dir := t.TempDir()
	tok, err := initializeTokenFile(dir, false)
	if err != nil {
		t.Fatalf("initializeTokenFile: %v", err)
	}
	if tok == "" {
		t.Fatalf("token must be non-empty")
	}

	path := filepath.Join(dir, "token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("perm: want 0600, got %o", mode)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if string(got) != tok {
		t.Errorf("file contents != returned token")
	}
}

func TestInit_ExistingFile_RefusesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	_, err := initializeTokenFile(dir, false)
	if err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err = initializeTokenFile(dir, false)
	if err == nil {
		t.Fatalf("second init should fail without --force")
	}
	if err != ErrAlreadyInitialized {
		t.Errorf("want ErrAlreadyInitialized, got %v", err)
	}
}

func TestInit_ExistingFile_OverwritesWithForce(t *testing.T) {
	dir := t.TempDir()
	tok1, err := initializeTokenFile(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := initializeTokenFile(dir, true)
	if err != nil {
		t.Fatalf("force init: %v", err)
	}
	if tok1 == tok2 {
		t.Errorf("force should mint a new token; both equal %q", tok1)
	}
}

func TestResolveToken_FilePresent(t *testing.T) {
	dir := t.TempDir()
	persisted, _ := initializeTokenFile(dir, false)
	os.Unsetenv("LLM_GATEWAY_TOKEN")

	tok, source, err := resolveToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tok != persisted {
		t.Errorf("token: want %q, got %q", persisted, tok)
	}
	if source != tokenSourceFile {
		t.Errorf("source: want %q, got %q", tokenSourceFile, source)
	}
}

func TestResolveToken_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	_, _ = initializeTokenFile(dir, false)
	t.Setenv("LLM_GATEWAY_TOKEN", "env-wins")

	tok, source, err := resolveToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "env-wins" {
		t.Errorf("token: want env-wins, got %q", tok)
	}
	if source != tokenSourceEnv {
		t.Errorf("source: want %q, got %q", tokenSourceEnv, source)
	}
}

func TestResolveToken_NeitherSet_Mints(t *testing.T) {
	dir := t.TempDir() // empty, no token file
	os.Unsetenv("LLM_GATEWAY_TOKEN")

	tok, source, err := resolveToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatalf("ephemeral token must be non-empty")
	}
	if source != tokenSourceEphemeral {
		t.Errorf("source: want %q, got %q", tokenSourceEphemeral, source)
	}
	// Ephemeral tokens are NOT persisted to disk — running init still required
	// for cross-restart stability.
	if _, err := os.Stat(filepath.Join(dir, "token")); err == nil {
		t.Errorf("ephemeral path should not create token file")
	}
}
