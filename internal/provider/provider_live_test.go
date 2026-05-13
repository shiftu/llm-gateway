//go:build live

package provider

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Run with: go test -tags live -v ./internal/provider/...
//
// Requires env: DEEPSEEK_API_KEY (and optionally DEEPSEEK_BASE_URL_OPENAI /
// _ANTHROPIC; defaults match docs/spikes/2026-05-13-deepseek-protocols.md).
//
// This is the real proof that Task 2a passthrough works end-to-end against
// a live LLM provider — not a stand-in. Excluded from `go test ./...` so
// CI runs clean without credentials.

func liveProvider(t *testing.T) *Provider {
	t.Helper()
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; skipping live test")
	}
	openai := envOr("DEEPSEEK_BASE_URL_OPENAI", "https://api.deepseek.com")
	anthropic := envOr("DEEPSEEK_BASE_URL_ANTHROPIC", "https://api.deepseek.com/anthropic")
	return &Provider{
		Name:             "deepseek",
		Kind:             "deepseek",
		OpenAIBaseURL:    openai,
		AnthropicBaseURL: anthropic,
		APIKey:           key,
	}
}

func envOr(k, dflt string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dflt
}

func TestLive_DeepSeek_OpenAIPassthrough_Blocking(t *testing.T) {
	p := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"reply with the single word: pong"}],"max_tokens":300,"stream":false}`)
	start := time.Now()
	resp, err := p.OpenAIRequest(ctx, body)
	if err != nil {
		t.Fatalf("OpenAIRequest: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, bs)
	}
	bs, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bs), "deepseek-v4-flash") {
		t.Errorf("response did not mention model name; body=%s", bs)
	}
	t.Logf("openai-passthrough latency: %s; %d bytes returned", elapsed, len(bs))
}

func TestLive_DeepSeek_AnthropicPassthrough_Blocking(t *testing.T) {
	p := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"reply with: pong"}],"max_tokens":300}`)
	start := time.Now()
	resp, err := p.AnthropicRequest(ctx, body)
	if err != nil {
		t.Fatalf("AnthropicRequest: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, bs)
	}
	bs, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bs), `"type":"message"`) {
		t.Errorf("response not Anthropic-shape; body=%s", bs)
	}
	// Spike F5: anthropic endpoint runs slower than openai. We don't assert
	// the 2.2× ratio here (too flaky), just log so we have evidence.
	t.Logf("anthropic-passthrough latency: %s; %d bytes returned", elapsed, len(bs))
}

func TestLive_DeepSeek_OpenAIPassthrough_Streaming(t *testing.T) {
	p := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"count: 1, 2, 3"}],"max_tokens":200,"stream":true}`)
	resp, err := p.OpenAIRequest(ctx, body)
	if err != nil {
		t.Fatalf("OpenAIRequest: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, bs)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("stream Content-Type: want text/event-stream*, got %q", ct)
	}
	bs, _ := io.ReadAll(resp.Body)
	// Spike F3: OpenAI stream uses flat `data: {chunk}\n\n` framing terminated
	// by `data: [DONE]`. Existence of at least one `data: ` line + the [DONE]
	// sentinel proves end-to-end stream fidelity.
	body_s := string(bs)
	if !strings.Contains(body_s, "data: ") || !strings.Contains(body_s, "[DONE]") {
		t.Errorf("stream body missing data: lines or [DONE] sentinel; body=%s", body_s)
	}
	t.Logf("openai-stream bytes=%d", len(bs))
}
