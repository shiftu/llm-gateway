package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestServer_AssembledRoutes is the integration sanity check covering plan
// §4 Task 1's end-to-end pipe: NewServer composes Auth middleware + mux +
// the two stub handlers, exposing Handler() for httptest.NewServer.
func TestServer_AssembledRoutes(t *testing.T) {
	srv := NewServer("test-token", nil)
	s := httptest.NewServer(srv.Handler())
	defer s.Close()

	cases := []struct {
		name       string
		path       string
		authHeader string
		authValue  string
		wantCode   int
	}{
		{"openai_valid_bearer", "/v1/chat/completions", "Authorization", "Bearer test-token", 200},
		{"anthropic_valid_xapikey", "/v1/messages", "x-api-key", "test-token", 200},
		{"openai_missing_auth", "/v1/chat/completions", "", "", 401},
		{"anthropic_wrong_xapikey", "/v1/messages", "x-api-key", "wrong", 401},
		{"unknown_route_returns_404", "/v1/embeddings", "Authorization", "Bearer test-token", 404},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, s.URL+c.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if c.authHeader != "" {
				req.Header.Set(c.authHeader, c.authValue)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.wantCode {
				t.Fatalf("want %d, got %d body=%s", c.wantCode, resp.StatusCode, string(body))
			}
		})
	}
}

// TestServer_Run_GracefulShutdown verifies F-1/F-18: SIGINT/ctx-cancel
// triggers graceful Server.Shutdown so in-flight requests can drain.
// We bind to :0 (kernel-chosen port), then cancel the context and ensure
// Run returns cleanly (not via os.Exit).
func TestServer_Run_GracefulShutdown(t *testing.T) {
	srv := NewServer("test-token", nil)
	ctx, cancel := context.WithCancel(context.Background())

	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.Run(ctx, "127.0.0.1:0")
	}()

	// Give Run a moment to bind; in real start.go this is the goroutine-launch
	// gap between server.ListenAndServe and ctx.Done dispatch. 50ms is generous
	// for localhost loopback.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel — shutdown wedged (F-1/F-18 regression)")
	}
}
