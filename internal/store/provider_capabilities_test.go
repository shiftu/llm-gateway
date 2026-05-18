package store

import (
	"errors"
	"testing"
)

func TestSetProviderCapability_InsertAndUpsert(t *testing.T) {
	st := openTest(t)

	// Insert new capability
	if err := st.SetProviderCapability("deepseek", "streaming", "true"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ok, val, err := st.HasCapabilityInStore("deepseek", "streaming")
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if !ok {
		t.Fatal("expected capability to exist after insert")
	}
	if val != "true" {
		t.Errorf("value: want %q, got %q", "true", val)
	}

	// Upsert — update the value
	if err := st.SetProviderCapability("deepseek", "streaming", "false"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_, val2, _ := st.HasCapabilityInStore("deepseek", "streaming")
	if val2 != "false" {
		t.Errorf("after upsert want %q, got %q", "false", val2)
	}
}

func TestListProviderCapabilities_EmptyForUnknown(t *testing.T) {
	st := openTest(t)

	caps, err := st.ListProviderCapabilities("ghost")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if caps == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(caps) != 0 {
		t.Errorf("expected 0 caps, got %d", len(caps))
	}
}

func TestListProviderCapabilities_NonEmpty(t *testing.T) {
	st := openTest(t)

	_ = st.SetProviderCapability("openai", "function_calling", "true")
	_ = st.SetProviderCapability("openai", "vision", "128k")

	caps, err := st.ListProviderCapabilities("openai")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("want 2 caps, got %d", len(caps))
	}
	// Check provider name populated
	for _, c := range caps {
		if c.ProviderName != "openai" {
			t.Errorf("provider name: want openai, got %q", c.ProviderName)
		}
		if c.UpdatedAt == "" {
			t.Error("updated_at should not be empty")
		}
	}
}

func TestDeleteProviderCapability_SuccessAndNotFound(t *testing.T) {
	st := openTest(t)

	_ = st.SetProviderCapability("anthropic", "reasoning", "extended")

	// Delete success
	if err := st.DeleteProviderCapability("anthropic", "reasoning"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	ok, _, _ := st.HasCapabilityInStore("anthropic", "reasoning")
	if ok {
		t.Error("capability should not exist after delete")
	}

	// Delete again → ErrNotFound
	err := st.DeleteProviderCapability("anthropic", "reasoning")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestHasCapabilityInStore_TrueAndFalse(t *testing.T) {
	st := openTest(t)

	ok, val, err := st.HasCapabilityInStore("nobody", "anything")
	if err != nil {
		t.Fatalf("has absent: %v", err)
	}
	if ok {
		t.Error("should be absent")
	}
	if val != "" {
		t.Errorf("absent value should be empty string, got %q", val)
	}

	_ = st.SetProviderCapability("gemini", "grounding", "")
	ok2, val2, err2 := st.HasCapabilityInStore("gemini", "grounding")
	if err2 != nil {
		t.Fatalf("has present: %v", err2)
	}
	if !ok2 {
		t.Error("should be present")
	}
	if val2 != "" {
		t.Errorf("stored empty value should return empty string, got %q", val2)
	}
}
