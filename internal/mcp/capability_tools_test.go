package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilityTools_SetAndList(t *testing.T) {
	st := openTestStore(t)
	setH := setProviderCapabilityHandler(st)
	listH := listProviderCapabilitiesHandler(st)

	// set_provider_capability
	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"provider_name": "deepseek",
		"capability":    "streaming",
		"value":         "true",
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set_provider_capability error: %v", res.Content)
	}
	body := textOf(t, res)
	if !strings.Contains(body, "streaming") || !strings.Contains(body, "deepseek") {
		t.Errorf("unexpected response text: %q", body)
	}

	// list_provider_capabilities — should return JSON array
	res2, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"provider_name": "deepseek",
	}))
	if err != nil {
		t.Fatalf("list handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("list_provider_capabilities error: %v", res2.Content)
	}
	var caps []capabilityJSON
	if err := json.Unmarshal([]byte(textOf(t, res2)), &caps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("want 1 capability, got %d", len(caps))
	}
	if caps[0].Capability != "streaming" || caps[0].Value != "true" {
		t.Errorf("round-trip mismatch: %+v", caps[0])
	}
	if caps[0].UpdatedAt == "" {
		t.Error("updated_at should not be empty")
	}
}

func TestCapabilityTools_ListEmpty(t *testing.T) {
	st := openTestStore(t)
	listH := listProviderCapabilitiesHandler(st)

	res, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"provider_name": "nobody",
	}))
	if err != nil {
		t.Fatalf("list handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("list error: %v", res.Content)
	}
	body := textOf(t, res)
	if !strings.Contains(body, "no capabilities") {
		t.Errorf("expected 'no capabilities' message, got %q", body)
	}
}

func TestCapabilityTools_SetDefaultValue(t *testing.T) {
	st := openTestStore(t)
	setH := setProviderCapabilityHandler(st)

	// value is optional — omitting it should default to ""
	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"provider_name": "anthropic",
		"capability":    "reasoning",
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set without value error: %v", res.Content)
	}

	ok, val, _ := st.HasCapabilityInStore("anthropic", "reasoning")
	if !ok {
		t.Fatal("capability should exist")
	}
	if val != "" {
		t.Errorf("value should be empty string, got %q", val)
	}
}

func TestCapabilityTools_ACL_AdminCanSet(t *testing.T) {
	st := openTestStore(t)
	setH := setProviderCapabilityHandler(st)

	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"provider_name": "openai",
		"capability":    "vision",
		"value":         "true",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Errorf("admin should be able to set: %v", res.Content)
	}
}

func TestCapabilityTools_ACL_AuditorCannotSet(t *testing.T) {
	st := openTestStore(t)
	setH := setProviderCapabilityHandler(st)

	res, err := setH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"provider_name": "openai",
		"capability":    "vision",
		"value":         "true",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Error("auditor should not be able to set capability")
	}
}

func TestCapabilityTools_ACL_AuditorCanList(t *testing.T) {
	st := openTestStore(t)
	_ = st.SetProviderCapability("openai", "vision", "true")
	listH := listProviderCapabilitiesHandler(st)

	res, err := listH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"provider_name": "openai",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Errorf("auditor should be able to list: %v", res.Content)
	}
}

func TestCapabilityTools_Upsert(t *testing.T) {
	st := openTestStore(t)
	setH := setProviderCapabilityHandler(st)

	// Insert
	_, _ = setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"provider_name": "openai", "capability": "context_length", "value": "8k",
	}))
	// Update
	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"provider_name": "openai", "capability": "context_length", "value": "128k",
	}))
	if err != nil {
		t.Fatalf("upsert handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("upsert error: %v", res.Content)
	}

	_, val, _ := st.HasCapabilityInStore("openai", "context_length")
	if val != "128k" {
		t.Errorf("after upsert want 128k, got %q", val)
	}
}
