package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

// mcp_test.go covers the /mcp Streamable HTTP transport end-to-end:
// it boots a full Server+MCPHandler, posts JSON-RPC over HTTP, and
// asserts auth + ACL behave the same as the stdio path.

const mcpLegacyToken = "legacy-mcp-tok"

func newMCPGateway(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := NewServer(mcpLegacyToken, s)
	srv.MountMCP(BuildMCPHandler(s))
	gw := httptest.NewServer(srv.Handler())
	t.Cleanup(gw.Close)
	return gw, s
}

// mcpPost issues a JSON-RPC POST to /mcp. Streamable HTTP requires the
// Accept header to advertise both JSON and SSE; otherwise mcp-go rejects
// the request as unsupported. authToken may be empty to test the 401 path.
func mcpPost(t *testing.T, gwURL, authToken, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, gwURL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	return resp
}

// mcpReadJSON drains a Streamable HTTP response that may be either a plain
// JSON body or a single-event SSE frame, and returns the JSON-RPC payload.
// mcp-go picks SSE when the client advertises text/event-stream; the event
// data is the JSON-RPC response on a `data: ` line.
func mcpReadJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	raw := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "data: ") {
				raw = []byte(strings.TrimPrefix(line, "data: "))
				break
			}
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode mcp response: %v\nraw: %s", err, body)
	}
	return m
}

func TestMCP_MissingAuth_Returns401(t *testing.T) {
	gw, _ := newMCPGateway(t)
	resp := mcpPost(t, gw.URL, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", resp.StatusCode)
	}
}

func TestMCP_LegacyToken_ToolsListIncludesSetModelCost(t *testing.T) {
	gw, _ := newMCPGateway(t)
	resp := mcpPost(t, gw.URL, mcpLegacyToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
	out := mcpReadJSON(t, resp)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", out)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not an array: %v", result)
	}
	want := map[string]bool{
		"ping":            false,
		"set_model_cost":  false,
		"list_model_costs": false,
		"add_provider":    false,
		"add_team":        false,
	}
	for _, t0 := range tools {
		m, _ := t0.(map[string]any)
		if name, _ := m["name"].(string); name != "" {
			if _, tracked := want[name]; tracked {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tools/list missing %q", name)
		}
	}
}

// TestMCP_LegacyToken_SetModelCost_Persists is the exact behaviour broken in
// production when stdio was the only transport: after a rebuild adding new
// tools, the HTTP path lets us call them without restarting Claude Code.
// Asserting end-to-end persistence here protects that contract.
func TestMCP_LegacyToken_SetModelCost_Persists(t *testing.T) {
	gw, st := newMCPGateway(t)
	body := `{
		"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{
			"name":"set_model_cost",
			"arguments":{
				"provider":"deepseek",
				"model":"deepseek-v4-flash",
				"usd_per_input_1k":0.000139,
				"usd_per_output_1k":0.000278,
				"usd_per_reasoning_1k":0.000278
			}
		}
	}`
	resp := mcpPost(t, gw.URL, mcpLegacyToken, body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, raw)
	}
	out := mcpReadJSON(t, resp)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %v", out)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("set_model_cost tool returned error: %v", result)
	}

	// asOf one hour in the future so the set_model_cost handler's
	// effective_from (time.Now() at insert) is guaranteed <= asOf,
	// regardless of UnixMilli rounding races on a fast machine.
	got, err := st.GetModelCost("deepseek", "deepseek-v4-flash", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("GetModelCost: %v\ntool result: %v", err, result)
	}
	if got.USDPerInput1k != 0.000139 || got.USDPerOutput1k != 0.000278 {
		t.Errorf("persisted price mismatch: %+v", got)
	}
	if got.USDPerReasoning1k == nil || *got.USDPerReasoning1k != 0.000278 {
		t.Errorf("persisted reasoning price mismatch: %+v", got)
	}
}

// TestMCP_LgwKey_AdminScope_CanCallAdminTool exercises the lgw_ auth path
// end-to-end: outer Auth.Middleware bcrypt-verifies the key and injects an
// APIKey into context; legacyKeyShim is a no-op; mcp-go forwards ctx to the
// tool handler; ACL passes because mcp_admin >= the tool's required scope.
func TestMCP_LgwKey_AdminScope_CanCallAdminTool(t *testing.T) {
	gw, st := newMCPGateway(t)
	tm, err := st.AddTeam("acme", "A")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := st.IssueAPIKey(tm.ID, "mcp_admin", "ci-bot")
	if err != nil {
		t.Fatal(err)
	}

	resp := mcpPost(t, gw.URL, plaintext, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_providers","arguments":{}}}`)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, raw)
	}
	out := mcpReadJSON(t, resp)
	if _, hasResult := out["result"]; !hasResult {
		t.Fatalf("admin tool unexpectedly errored: %v", out)
	}
}

// TestMCP_LgwKey_InboundScope_DeniedOnAdminTool ensures the ACL boundary
// holds over HTTP: an inbound-scope key (typical end-user) can't drive
// admin tools.
func TestMCP_LgwKey_InboundScope_DeniedOnAdminTool(t *testing.T) {
	gw, st := newMCPGateway(t)
	tm, err := st.AddTeam("acme", "A")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := st.IssueAPIKey(tm.ID, "inbound", "user")
	if err != nil {
		t.Fatal(err)
	}

	resp := mcpPost(t, gw.URL, plaintext, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_providers","arguments":{}}}`)
	// ACL denial surfaces as an MCP tool error, NOT an HTTP error — the JSON-RPC
	// envelope succeeds; the tool result carries the permission-denied message.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, raw)
	}
	out := mcpReadJSON(t, resp)
	// Reading the result.content[0].text — that's where MCP tool errors land
	// when the handler returns (nil, err). Permission-denied has that shape.
	raw, _ := json.Marshal(out)
	if !bytes.Contains(raw, []byte("permission denied")) {
		t.Errorf("expected permission denied in response, got: %s", raw)
	}
}
