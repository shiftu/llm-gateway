package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTypeSafeMCPSetup(t *testing.T) {
	st := openTestStore(t)
	ctx := ctxWithScope("mcp_admin")
	res, err := addProviderHandler(st)(ctx, callTool(map[string]any{
		"name": "typesafe", "kind": "typesafe", "api_key": "typesafe-secret-123456",
		"typesafe_base_url": "https://api.typesafe.ai/v1",
	}))
	if err != nil || res.IsError {
		t.Fatalf("add: %+v %v", res, err)
	}
	p, err := st.GetProvider("typesafe")
	if err != nil || p.TypeSafeBaseURL != "https://api.typesafe.ai/v1" || p.OpenAIBaseURL != "" {
		t.Fatalf("provider: %+v %v", p, err)
	}
	res, err = setModelAliasHandler(st)(ctx, callTool(map[string]any{"alias": "evaluate", "provider_name": "typesafe", "upstream_model": "jev-latest"}))
	if err != nil || res.IsError {
		t.Fatalf("alias: %+v %v", res, err)
	}
	res, err = listProvidersHandler(st)(ctx, callTool(nil))
	if err != nil || res.IsError {
		t.Fatalf("list: %+v %v", res, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "typesafe_base_url") || strings.Contains(string(b), p.APIKey) {
		t.Fatalf("list metadata/masking: %s", b)
	}
	contents, err := providersHandler(st)(ctx, readResource("lgw://providers"))
	if err != nil {
		t.Fatal(err)
	}
	resource := textOfResource(t, contents)
	if !strings.Contains(resource, `"has_typesafe_url": true`) && !strings.Contains(resource, `"has_typesafe_url":true`) {
		t.Fatalf("missing protocol metadata: %s", resource)
	}
	if strings.Contains(resource, p.APIKey) {
		t.Fatal("provider resource exposed API key")
	}
	prompt, err := addProviderWizardHandler()(ctx, promptReq("add_provider_wizard", map[string]string{"kind": "typesafe"}))
	if err != nil {
		t.Fatal(err)
	}
	text := firstMsgText(t, prompt)
	if !strings.Contains(text, "typesafe_base_url") || !strings.Contains(text, "/v1/systemone") || strings.Contains(text, "openai_base_url") {
		t.Fatalf("wrong wizard: %s", text)
	}
}
