package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

// responses_test.go validates the /v1/responses end-to-end lifecycle:
// Responses body in → ir.ChatRequestFromResponses → router.ResolveForTeam →
// upstream Chat Completions SSE → ir.ResponsesStreamState → Responses SSE out.
// Contract: docs/design/responses-api.md.

func TestServer_Responses_PureTextTurn_FullSSEContract(t *testing.T) {
	upstream := &flushingSSEHandler{
		chunks: []string{
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"PONG\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n",
			"data: [DONE]\n\n",
		},
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{
		"model": "deepseek-v4-flash",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"say PONG"}]}],
		"stream": true
	}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/responses", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: want text/event-stream*, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	// The upstream sent us plain Chat SSE. What the client (codex) sees back
	// must be the *translated* Responses event names, never the raw Chat shape.
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "chat.completion.chunk") {
		t.Errorf("raw Chat SSE leaked through untranslated:\n%s", out)
	}
	if !strings.Contains(out, `"text":"PONG"`) {
		t.Errorf("translated text %q not found in output:\n%s", "PONG", out)
	}
	if !strings.Contains(out, `"input_tokens":7`) || !strings.Contains(out, `"output_tokens":3`) {
		t.Errorf("usage not translated into response.completed:\n%s", out)
	}
	// response.completed must be the LAST event codex sees, or it retries.
	lastEvent := out[strings.LastIndex(out, "event: "):]
	if !strings.HasPrefix(lastEvent, "event: response.completed") {
		t.Errorf("response.completed is not the final event:\n%s", out)
	}

	time.Sleep(20 * time.Millisecond)
	logs, _ := s.TailLogs(10)
	if len(logs) != 1 || logs[0].Status != "ok" || logs[0].PromptTokens != 7 || logs[0].CompletionTokens != 3 {
		t.Errorf("request not logged with translated usage: %+v", logs)
	}
}

func TestServer_Responses_ToolCallTurn(t *testing.T) {
	upstream := &flushingSSEHandler{
		chunks: []string{
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"\"}}]}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"cmd\\\":\\\"echo hi\\\"}\"}}]}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1}}\n\n",
			"data: [DONE]\n\n",
		},
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{
		"model": "deepseek-v4-flash",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"run echo"}]}],
		"tools": [{"type":"function","name":"bash","description":"run a shell command","parameters":{"type":"object"}}],
		"stream": true
	}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/responses", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	for _, want := range []string{
		"event: response.output_item.added",
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"call_id":"call_1"`) {
		t.Errorf("call_id from upstream tool_calls[].id not carried through:\n%s", out)
	}
	if !strings.Contains(out, `\"cmd\":\"echo hi\"`) {
		t.Errorf("arguments JSON string not carried through verbatim:\n%s", out)
	}
}

func TestServer_Responses_EmptyInput_Returns400(t *testing.T) {
	upSrv := httptest.NewServer(&mockUpstream{respCT: "application/json", respBody: `{}`})
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/responses",
		strings.NewReader(`{"model":"deepseek-v4-flash","input":[]}`))
	req.Header.Set("Authorization", "Bearer gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_responses_input") {
		t.Errorf("missing invalid_responses_input tag: %s", body)
	}
}

func TestServer_Responses_NoRoute_Returns404(t *testing.T) {
	// Provider exists but is NOT default; "unknown" matches no alias.
	s, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = s.Close() })
	_ = s.AddProvider(store.Provider{
		Name: "deepseek", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "k",
		IsDefault:     false,
	})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/responses",
		strings.NewReader(`{"model":"unknown","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", resp.StatusCode)
	}
}
