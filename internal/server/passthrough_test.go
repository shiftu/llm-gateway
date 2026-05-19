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

// passthrough_test.go validates the end-to-end store-driven request lifecycle:
// inbound body parsed → router.Resolve → provider snapshot → upstream call
// → byte-fidelity response copy → request log row.

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

// newStoreWithDefaultProvider wires a fresh in-memory store with one
// provider as default. opts let tests override base URLs to point at an
// httptest fake.
type storeOpts struct {
	openaiURL    string
	anthropicURL string
}

func newStoreWithDefaultProvider(t *testing.T, name string, opts storeOpts) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AddProvider(store.Provider{
		Name:             name,
		Kind:             "deepseek",
		OpenAIBaseURL:    opts.openaiURL,
		AnthropicBaseURL: opts.anthropicURL,
		APIKey:           "ds-test",
		IsDefault:        true,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServer_OpenAIPassthrough_DefaultRoute_ForwardsToProvider(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"upstream-1","choices":[{"message":{"content":"hi"}}]}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer gw-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"upstream-1"`) {
		t.Errorf("upstream body not piped back: %s", body)
	}
	// Upstream URL is /v1/chat/completions, Authorization is Bearer ds-test
	if upstream.gotURL != "/v1/chat/completions" {
		t.Errorf("upstream URL: %s", upstream.gotURL)
	}
	if got := upstream.gotHeader.Get("Authorization"); got != "Bearer ds-test" {
		t.Errorf("upstream Authorization: want %q, got %q", "Bearer ds-test", got)
	}

	// Wait briefly to let the async-style log write commit (it's synchronous,
	// but the test framework runs concurrently — keep simple).
	time.Sleep(20 * time.Millisecond)
	logs, _ := s.TailLogs(10)
	if len(logs) != 1 || logs[0].Status != "ok" || logs[0].ProviderName != "deepseek" {
		t.Errorf("request not logged: %+v", logs)
	}
}

func TestServer_OpenAIPassthrough_AliasRewritesModel(t *testing.T) {
	upstream := &mockUpstream{respCT: "application/json", respBody: `{"ok":true}`}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	if err := s.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil); err != nil {
		t.Fatal(err)
	}
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	// Client asks for "fast" — gateway must rewrite to "deepseek-v4-flash".
	clientBody := `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	if !strings.Contains(string(upstream.gotBody), `"deepseek-v4-flash"`) {
		t.Errorf("upstream did not see rewritten model: %s", upstream.gotBody)
	}
	if strings.Contains(string(upstream.gotBody), `"fast"`) {
		t.Errorf("upstream saw client-side alias 'fast' instead of resolved upstream model: %s", upstream.gotBody)
	}
}

func TestServer_AnthropicPassthrough_DefaultRoute(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"upstream-2","type":"message"}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{anthropicURL: upSrv.URL})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	clientBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/messages", strings.NewReader(clientBody))
	req.Header.Set("x-api-key", "gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if upstream.gotURL != "/v1/messages" {
		t.Errorf("upstream URL: %s", upstream.gotURL)
	}
	if got := upstream.gotHeader.Get("x-api-key"); got != "ds-test" {
		t.Errorf("upstream x-api-key: %s", got)
	}
}

func TestServer_CrossProtocol_ReturnsNotImplemented(t *testing.T) {
	// Provider has only OpenAI base_url; client hits /v1/messages (Anthropic).
	// Until Task 2b IR translation lands, gateway returns 501 with a clear hint.
	upSrv := httptest.NewServer(&mockUpstream{respCT: "application/json", respBody: `{}`})
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/messages",
		strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
	req.Header.Set("x-api-key", "gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status: want 501, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "cross_protocol_not_supported") {
		t.Errorf("missing cross_protocol_not_supported tag: %s", body)
	}
}

func TestServer_NoRoute_Returns404(t *testing.T) {
	// Store with provider but NOT default. No alias matches. → ErrNoRoute.
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

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"unknown","messages":[]}`))
	req.Header.Set("Authorization", "Bearer gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no_route") {
		t.Errorf("missing no_route: %s", body)
	}
}

// TestServer_OpenAIPassthrough_SSEStreamFidelity covers spike F3 + F-Q16:
// when upstream emits text/event-stream, the gateway preserves the
// Content-Type, flushes per chunk, and forwards bytes verbatim.
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

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL + "/v1"})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
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

func TestServer_NilStore_StubMode(t *testing.T) {
	srv := NewServer("gw-token", nil)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "stub response from llm-gateway") {
		t.Errorf("expected stub response, got: %s", body)
	}
}
