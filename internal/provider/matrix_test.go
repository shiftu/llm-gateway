package provider

// matrix_test.go: T14 — provider pass-through fidelity matrix.
//
// Matrix: 3 providers × 3 response shapes = 9 subtests.
// Providers: deepseek, qwen, moonshot (all OpenAI-compat via OpenAIRequest).
// Shapes:
//   - reasoning: response contains reasoning_content (DeepSeek-style)
//   - tools:     response contains tool_calls
//   - stream:    text/event-stream SSE with data: lines + [DONE]
//
// Tests use httptest servers serving golden fixtures from testdata/.
// All assertions are byte-fidelity: the pass-through layer MUST NOT modify
// response bytes, preserving every field regardless of provider.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var matrixProviders = []struct{ name, kind string }{
	{"deepseek", "deepseek"},
	{"qwen", "qwen"},
	{"moonshot", "moonshot"},
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func serveBytes(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

func TestMatrix_ReasoningPassthrough(t *testing.T) {
	fixture := readFixture(t, "reasoning_response.json")
	for _, p := range matrixProviders {
		p := p
		t.Run(p.name, func(t *testing.T) {
			upstream := serveBytes(t, "application/json", fixture)
			defer upstream.Close()

			pr := &Provider{Name: p.name, Kind: p.kind, OpenAIBaseURL: upstream.URL, APIKey: "k"}
			resp, err := pr.OpenAIRequest(context.Background(), strings.NewReader(`{"model":"test","messages":[]}`))
			if err != nil {
				t.Fatalf("OpenAIRequest: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != string(fixture) {
				t.Errorf("reasoning_content not preserved\nwant: %s\n got: %s", fixture, got)
			}
		})
	}
}

func TestMatrix_ToolsPassthrough(t *testing.T) {
	fixture := readFixture(t, "tools_response.json")
	for _, p := range matrixProviders {
		p := p
		t.Run(p.name, func(t *testing.T) {
			upstream := serveBytes(t, "application/json", fixture)
			defer upstream.Close()

			pr := &Provider{Name: p.name, Kind: p.kind, OpenAIBaseURL: upstream.URL, APIKey: "k"}
			resp, err := pr.OpenAIRequest(context.Background(), strings.NewReader(`{"model":"test","messages":[],"tools":[]}`))
			if err != nil {
				t.Fatalf("OpenAIRequest: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != string(fixture) {
				t.Errorf("tool_calls not preserved\nwant: %s\n got: %s", fixture, got)
			}
		})
	}
}

func TestMatrix_StreamPassthrough(t *testing.T) {
	fixture := readFixture(t, "stream_response.sse")
	for _, p := range matrixProviders {
		p := p
		t.Run(p.name, func(t *testing.T) {
			upstream := serveBytes(t, "text/event-stream", fixture)
			defer upstream.Close()

			pr := &Provider{Name: p.name, Kind: p.kind, OpenAIBaseURL: upstream.URL, APIKey: "k"}
			resp, err := pr.OpenAIRequest(context.Background(), strings.NewReader(`{"model":"test","messages":[],"stream":true}`))
			if err != nil {
				t.Fatalf("OpenAIRequest: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "text/event-stream") {
				t.Errorf("Content-Type: want text/event-stream, got %q", ct)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != string(fixture) {
				t.Errorf("stream bytes not preserved\nwant: %s\n got: %s", fixture, got)
			}
		})
	}
}
