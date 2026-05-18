//go:build live

package provider

// Run with: go test -tags live -v ./internal/provider/... -run Moonshot
//
// Requires env: MOONSHOT_API_KEY
// Optional:     MOONSHOT_BASE_URL (defaults to https://api.moonshot.cn)

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func moonshotProvider(t *testing.T) *Provider {
	t.Helper()
	key := os.Getenv("MOONSHOT_API_KEY")
	if key == "" {
		t.Skip("MOONSHOT_API_KEY not set; skipping live test")
	}
	baseURL := envOr("MOONSHOT_BASE_URL", "https://api.moonshot.cn")
	return &Provider{
		Name:          "moonshot",
		Kind:          "moonshot",
		OpenAIBaseURL: baseURL,
		APIKey:        key,
	}
}

func TestLive_Moonshot_OpenAIPassthrough_Blocking(t *testing.T) {
	p := moonshotProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	body := strings.NewReader(`{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"reply with the single word: pong"}],"max_tokens":100,"stream":false}`)
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
	if !strings.Contains(string(bs), "moonshot-v1-8k") {
		t.Errorf("response did not mention model name; body=%s", bs)
	}
	t.Logf("openai-passthrough latency: %s; %d bytes returned", elapsed, len(bs))
}

func TestLive_Moonshot_OpenAIPassthrough_Streaming(t *testing.T) {
	p := moonshotProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	body := strings.NewReader(`{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"count: 1, 2, 3"}],"max_tokens":100,"stream":true}`)
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
	bodyStr := string(bs)
	if !strings.Contains(bodyStr, "data: ") || !strings.Contains(bodyStr, "[DONE]") {
		t.Errorf("stream body missing data: lines or [DONE] sentinel; body=%s", bodyStr)
	}
	t.Logf("openai-stream bytes=%d", len(bs))
}
