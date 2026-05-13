package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/provider"
)

// passthrough_test.go validates that when a Server is constructed with a
// concrete provider, /v1/chat/completions and /v1/messages forward inbound
// bodies upstream byte-for-byte and stream the response back without
// re-encoding (plan R2 pass-through path; spike F-Q16 byte fidelity).
//
// We spin up an httptest.Server as a fake upstream — provider hits that
// instead of the real DeepSeek API. End-to-end real-provider tests live in
// internal/provider/provider_live_test.go (build tag: live).

type mockUpstream struct {
	gotURL    string
	gotHeader http.Header
	gotBody   []byte
	respCode  int
	respCT    string
	respBody  string
}

func (u *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.gotURL = r.URL.String()
	u.gotHeader = r.Header.Clone()
	u.gotBody, _ = io.ReadAll(r.Body)

	if u.respCode == 0 {
		u.respCode = http.StatusOK
	}
	if u.respCT != "" {
		w.Header().Set("Content-Type", u.respCT)
	}
	w.WriteHeader(u.respCode)
	_, _ = io.WriteString(w, u.respBody)
}

func TestServer_OpenAIPassthrough_ForwardsToProvider(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"upstream-1","choices":[{"message":{"content":"hi"}}]}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	prov := &provider.Provider{
		Name:          "deepseek",
		Kind:          "deepseek",
		OpenAIBaseURL: upSrv.URL,
		APIKey:        "ds-test",
	}
	srv := NewServer("gw-token", prov)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer gw-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"upstream-1"`) {
		t.Errorf("upstream body not piped back: %s", body)
	}
	// Upstream saw exact client body
	if string(upstream.gotBody) != clientBody {
		t.Errorf("body mutated through gateway:\n  want: %s\n  got:  %s", clientBody, upstream.gotBody)
	}
	// Upstream URL was /v1/chat/completions, auth Bearer was replaced
	// with the provider's key (not the gateway's bearer)
	if upstream.gotURL != "/v1/chat/completions" {
		t.Errorf("upstream URL: %s", upstream.gotURL)
	}
	if got := upstream.gotHeader.Get("Authorization"); got != "Bearer ds-test" {
		t.Errorf("upstream Authorization: want %q, got %q", "Bearer ds-test", got)
	}
}

func TestServer_AnthropicPassthrough_ForwardsToProvider(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"upstream-2","type":"message","content":[{"type":"text","text":"hi"}]}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	prov := &provider.Provider{
		Name:             "deepseek",
		Kind:             "deepseek",
		AnthropicBaseURL: upSrv.URL,
		APIKey:           "ds-test",
		AnthropicVersion: "2023-06-01",
	}
	srv := NewServer("gw-token", prov)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/messages", strings.NewReader(clientBody))
	req.Header.Set("x-api-key", "gw-token")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"upstream-2"`) {
		t.Errorf("upstream body not piped back: %s", body)
	}
	if upstream.gotURL != "/v1/messages" {
		t.Errorf("upstream URL: %s", upstream.gotURL)
	}
	if got := upstream.gotHeader.Get("x-api-key"); got != "ds-test" {
		t.Errorf("upstream x-api-key: want %q, got %q", "ds-test", got)
	}
	if got := upstream.gotHeader.Get("Authorization"); got != "" {
		t.Errorf("Authorization MUST NOT leak to anthropic upstream; got %q", got)
	}
}

// TestServer_OpenAIPassthrough_SSEStreamFidelity covers spike F3 + F-Q16:
// when upstream emits text/event-stream, the gateway must (a) preserve the
// Content-Type, (b) flush chunks as they arrive (not buffer until close),
// and (c) emit upstream bytes verbatim.
func TestServer_OpenAIPassthrough_SSEStreamFidelity(t *testing.T) {
	upstream := &flushingSSEHandler{
		chunks: []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
			"data: [DONE]\n\n",
		},
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	prov := &provider.Provider{
		Name:          "deepseek",
		Kind:          "deepseek",
		OpenAIBaseURL: upSrv.URL,
		APIKey:        "ds-test",
	}
	srv := NewServer("gw-token", prov)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type: want text/event-stream*, got %q", got)
	}

	body, _ := io.ReadAll(resp.Body)
	want := strings.Join(upstream.chunks, "")
	if string(body) != want {
		t.Errorf("stream body byte mismatch:\n  want: %q\n  got:  %q", want, body)
	}
}

// flushingSSEHandler emits chunks one at a time with explicit flushes so the
// downstream pipeline has a chance to surface buffering bugs. If the gateway
// were to read the whole upstream body before writing, this test still passes
// (we read full body) — but a streaming-fidelity inspector with timing would
// catch it. We assert byte-equality which is the v0.1 floor.
type flushingSSEHandler struct {
	chunks []string
}

func (h *flushingSSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, c := range h.chunks {
		_, _ = io.WriteString(w, c)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestServer_NilProvider_StubMode preserves the original Task 1 behaviour:
// when the Server is built without a provider (stub mode for offline dev),
// handlers return the hardcoded JSON they always did. Backwards-compat sanity.
func TestServer_NilProvider_StubMode(t *testing.T) {
	srv := NewServer("gw-token", nil)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "stub response from llm-gateway") {
		t.Errorf("expected stub response, got: %s", body)
	}
}
