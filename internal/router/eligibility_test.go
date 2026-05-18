package router

import (
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviderHasCapability_True(t *testing.T) {
	st := openTestStore(t)
	if err := st.SetProviderCapability("openai", "function_calling", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !ProviderHasCapability(st, "openai", "function_calling") {
		t.Error("expected true for existing capability")
	}
}

func TestProviderHasCapability_False_Absent(t *testing.T) {
	st := openTestStore(t)
	if ProviderHasCapability(st, "openai", "nonexistent") {
		t.Error("expected false for absent capability")
	}
}

func TestProviderHasCapability_False_WrongProvider(t *testing.T) {
	st := openTestStore(t)
	if err := st.SetProviderCapability("openai", "vision", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ProviderHasCapability(st, "anthropic", "vision") {
		t.Error("expected false for different provider")
	}
}
