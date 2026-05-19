package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingHandler captures inbound requests so the test can assert URL,
// headers, and body. It echoes a small JSON body back so the caller can
// read it as if it were a real upstream.
type recordingHandler struct {
	gotURL    string
	gotMethod string
	gotHeader http.Header
	gotBody   []byte
	respCode  int
	respBody  string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotURL = r.URL.String()
	h.gotMethod = r.Method
	h.gotHeader = r.Header.Clone()
	h.gotBody, _ = io.ReadAll(r.Body)

	if h.respCode == 0 {
		h.respCode = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(h.respCode)
	if h.respBody == "" {
		h.respBody = `{"ok":true}`
	}
	_, _ = io.WriteString(w, h.respBody)
}

func TestProvider_OpenAIRequest_BuildsCorrectURLAndHeaders(t *testing.T) {
	h := &recordingHandler{}
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	p := &Provider{
		Name:          "deepseek",
		Kind:          "deepseek",
		OpenAIBaseURL: upstream.URL + "/v1", // base URL includes /v1 per OpenAI SDK convention
		APIKey:        "ds-test-key",
	}

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := p.OpenAIRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("OpenAIRequest: %v", err)
	}
	defer resp.Body.Close()

	if h.gotMethod != http.MethodPost {
		t.Errorf("method: want POST, got %s", h.gotMethod)
	}
	if h.gotURL != "/v1/chat/completions" {
		t.Errorf("URL: want /v1/chat/completions, got %s", h.gotURL)
	}
	if got := h.gotHeader.Get("Authorization"); got != "Bearer ds-test-key" {
		t.Errorf("Authorization: want %q, got %q", "Bearer ds-test-key", got)
	}
	if got := h.gotHeader.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", got)
	}
	if !strings.Contains(string(h.gotBody), "deepseek-v4-flash") {
		t.Errorf("body not forwarded verbatim: %s", h.gotBody)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", resp.StatusCode)
	}
}

func TestProvider_AnthropicRequest_BuildsCorrectURLAndHeaders(t *testing.T) {
	h := &recordingHandler{}
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	p := &Provider{
		Name:             "deepseek",
		Kind:             "deepseek",
		AnthropicBaseURL: upstream.URL,
		APIKey:           "ds-test-key",
		AnthropicVersion: "2023-06-01",
	}

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)
	resp, err := p.AnthropicRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("AnthropicRequest: %v", err)
	}
	defer resp.Body.Close()

	if h.gotURL != "/v1/messages" {
		t.Errorf("URL: want /v1/messages, got %s", h.gotURL)
	}
	if got := h.gotHeader.Get("x-api-key"); got != "ds-test-key" {
		t.Errorf("x-api-key: want %q, got %q", "ds-test-key", got)
	}
	if got := h.gotHeader.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version: want %q, got %q", "2023-06-01", got)
	}
	if got := h.gotHeader.Get("Authorization"); got != "" {
		t.Errorf("Authorization must NOT be sent on Anthropic endpoint; got %q", got)
	}
}

func TestProvider_OpenAIRequest_MissingBaseURL_Errors(t *testing.T) {
	p := &Provider{Name: "deepseek", Kind: "deepseek", APIKey: "x"}
	_, err := p.OpenAIRequest(context.Background(), strings.NewReader(`{}`))
	if !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("want ErrProtocolUnsupported, got %v", err)
	}
}

func TestProvider_AnthropicRequest_MissingBaseURL_Errors(t *testing.T) {
	p := &Provider{Name: "deepseek", Kind: "deepseek", APIKey: "x"}
	_, err := p.AnthropicRequest(context.Background(), strings.NewReader(`{}`))
	if !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("want ErrProtocolUnsupported, got %v", err)
	}
}

func TestProvider_OpenAIRequest_BodyByteFidelity(t *testing.T) {
	// Spike F-Q16: pass-through MUST forward the inbound body byte-for-byte
	// so the upstream sees exactly what the client sent. The test asserts
	// no JSON re-encoding (which would normalise whitespace / field order).
	h := &recordingHandler{}
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	p := &Provider{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: upstream.URL, APIKey: "k"}
	verbatim := []byte(`{"model":"x", "messages":[{"role":"user","content":"hi"}], "stream":true}`)
	_, err := p.OpenAIRequest(context.Background(), strings.NewReader(string(verbatim)))
	if err != nil {
		t.Fatalf("OpenAIRequest: %v", err)
	}
	if string(h.gotBody) != string(verbatim) {
		t.Errorf("body re-encoded:\n  want: %s\n  got:  %s", verbatim, h.gotBody)
	}
}

// TestProvider_OpenAIRequest_BaseURLIncludesV1: per OpenAI SDK convention the
// base URL already contains the version prefix. When stored as
// "…/v1", the gateway appends only "/chat/completions" — no double /v1.
func TestProvider_OpenAIRequest_BaseURLIncludesV1(t *testing.T) {
	h := &recordingHandler{}
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	p := &Provider{
		Name:          "qwen",
		Kind:          "qwen",
		OpenAIBaseURL: upstream.URL + "/v1",
		APIKey:        "qwen-key",
	}
	_, err := p.OpenAIRequest(context.Background(), strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("OpenAIRequest: %v", err)
	}
	if h.gotURL != "/v1/chat/completions" {
		t.Errorf("URL: want /v1/chat/completions, got %s", h.gotURL)
	}
}

func TestProvider_AnthropicRequest_UsesDefaultVersionWhenUnset(t *testing.T) {
	h := &recordingHandler{}
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	p := &Provider{Name: "x", Kind: "deepseek", AnthropicBaseURL: upstream.URL, APIKey: "k"}
	// Note: AnthropicVersion omitted on purpose.
	_, err := p.AnthropicRequest(context.Background(), strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("AnthropicRequest: %v", err)
	}
	if got := h.gotHeader.Get("anthropic-version"); got != DefaultAnthropicVersion {
		t.Errorf("anthropic-version: want default %q, got %q", DefaultAnthropicVersion, got)
	}
}
