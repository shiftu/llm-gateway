package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// TestServer_FallbackOnHttp5xx verifies that a 5xx from the primary provider
// triggers a specific_provider fallback and the client receives the secondary
// provider's successful response.
func TestServer_FallbackOnHttp5xx(t *testing.T) {
	primary := &mockUpstream{respCode: http.StatusServiceUnavailable, respCT: "application/json", respBody: `{"error":"down"}`}
	secondary := &mockUpstream{respCode: http.StatusOK, respCT: "application/json", respBody: `{"id":"fallback-ok","choices":[]}`}

	primarySrv := httptest.NewServer(primary)
	defer primarySrv.Close()
	secondarySrv := httptest.NewServer(secondary)
	defer secondarySrv.Close()

	s, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = s.Close() })
	_ = s.AddProvider(store.Provider{
		Name: "primary", Kind: "deepseek",
		OpenAIBaseURL: primarySrv.URL, APIKey: "k1", IsDefault: true,
	})
	_ = s.AddProvider(store.Provider{
		Name: "secondary", Kind: "deepseek",
		OpenAIBaseURL: secondarySrv.URL, APIKey: "k2",
	})
	_, _ = s.SetFallbackPolicy(store.FallbackPolicy{
		Trigger: "http_5xx", Action: "specific_provider",
		TargetProvider: "secondary", MaxChainDepth: 3,
	})

	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"any","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 after fallback, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fallback-ok") {
		t.Errorf("expected secondary response body, got: %s", body)
	}
	// Primary must have been hit (it triggered the fallback)
	if len(primary.gotBody) == 0 {
		t.Error("primary provider was never called")
	}
}

// TestServer_FallbackOnHttp429 verifies the http_429 trigger path.
func TestServer_FallbackOnHttp429(t *testing.T) {
	primary := &mockUpstream{respCode: http.StatusTooManyRequests, respCT: "application/json", respBody: `{"error":"rate limited"}`}
	secondary := &mockUpstream{respCode: http.StatusOK, respCT: "application/json", respBody: `{"id":"retry-ok","choices":[]}`}

	primarySrv := httptest.NewServer(primary)
	defer primarySrv.Close()
	secondarySrv := httptest.NewServer(secondary)
	defer secondarySrv.Close()

	s, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = s.Close() })
	_ = s.AddProvider(store.Provider{
		Name: "primary", Kind: "deepseek",
		OpenAIBaseURL: primarySrv.URL, APIKey: "k1", IsDefault: true,
	})
	_ = s.AddProvider(store.Provider{
		Name: "secondary", Kind: "deepseek",
		OpenAIBaseURL: secondarySrv.URL, APIKey: "k2",
	})
	_, _ = s.SetFallbackPolicy(store.FallbackPolicy{
		Trigger: "http_429", Action: "specific_provider",
		TargetProvider: "secondary", MaxChainDepth: 3,
	})

	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"any","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 after fallback, got %d: %s", resp.StatusCode, body)
	}
}

// TestServer_NoFallbackPolicy_PassesThrough5xx verifies that without a policy
// the original 5xx response reaches the client unchanged.
func TestServer_NoFallbackPolicy_PassesThrough5xx(t *testing.T) {
	primary := &mockUpstream{respCode: http.StatusBadGateway, respCT: "application/json", respBody: `{"error":"bad gw"}`}
	upSrv := httptest.NewServer(primary)
	defer upSrv.Close()

	s := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL})
	srv := NewServer("gw-token", s)
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"any","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gw-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 passthrough, got %d", resp.StatusCode)
	}
}
