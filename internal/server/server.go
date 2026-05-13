package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/panda/llm-gateway/internal/provider"
)

// Server composes the bearer-auth middleware with the inbound mux. v0.1
// scope: 2 routes (OpenAI + Anthropic) + optional upstream provider for
// pass-through. When prov is nil the handlers return stub JSON (offline
// dev / smoke). When prov is set, R2 pass-through forwards bytes to the
// upstream provider's matching protocol endpoint.
type Server struct {
	auth     *Auth
	provider *provider.Provider // nil = stub mode
	mux      *http.ServeMux
}

// NewServer builds the mux and wires both inbound endpoints. prov can be
// nil to run in stub mode (Task 1 behaviour preserved for tests/smoke).
func NewServer(token string, prov *provider.Provider) *Server {
	s := &Server{
		auth:     NewAuth(token),
		provider: prov,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("/v1/chat/completions", s.dispatchChatCompletions)
	s.mux.HandleFunc("/v1/messages", s.dispatchMessages)
	return s
}

// dispatchChatCompletions sends to passthrough when a provider is wired,
// otherwise returns the Task-1 stub. Same shape for /v1/messages below.
func (s *Server) dispatchChatCompletions(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		HandleChatCompletions(w, r)
		return
	}
	s.passthroughOpenAI(w, r)
}

func (s *Server) dispatchMessages(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		HandleMessages(w, r)
		return
	}
	s.passthroughAnthropic(w, r)
}

func (s *Server) passthroughOpenAI(w http.ResponseWriter, r *http.Request) {
	resp, err := s.provider.OpenAIRequest(r.Context(), r.Body)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	defer resp.Body.Close()
	pipeUpstream(w, resp)
}

func (s *Server) passthroughAnthropic(w http.ResponseWriter, r *http.Request) {
	resp, err := s.provider.AnthropicRequest(r.Context(), r.Body)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	defer resp.Body.Close()
	pipeUpstream(w, resp)
}

// pipeUpstream copies the upstream HTTP response onto the inbound
// ResponseWriter with byte fidelity. SSE streams are flushed per chunk so
// the client sees tokens as they arrive (spike F3 / F-Q16).
func pipeUpstream(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		flushAndCopy(w, resp.Body)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func flushAndCopy(w http.ResponseWriter, r io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// writeUpstreamError surfaces a structured error for upstream-side failures
// (timeout, network, provider returned non-2xx pre-handler errors). Uses
// the same F-DX-05 shape as auth errors for uniformity.
func writeUpstreamError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if errors.Is(err, provider.ErrProtocolUnsupported) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"type":"no_route","message":"provider does not support this inbound protocol natively","fix":"register a provider with the matching base_url, or wait for IR translation in v0.2"}}`)
		return
	}
	w.WriteHeader(http.StatusBadGateway)
	_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","message":"failed to reach upstream provider","fix":"check provider base_url + network; tail recent request_logs for details"}}`)
}

// Handler returns the fully composed http.Handler — auth middleware wrapping
// the route mux. Exposed so tests can httptest.NewServer it without touching
// network listeners.
func (s *Server) Handler() http.Handler {
	return s.auth.Middleware(s.mux)
}

// Run starts the HTTP server on addr and blocks until ctx is cancelled. On
// cancel it runs http.Server.Shutdown with a 30-second grace window (per
// plan F-1/F-18 5-step shutdown sequence — Task 1 only owns step 1 + step 3
// of that sequence; MCP/SQLite drain land in later tasks).
//
// addr "127.0.0.1:0" is honoured for tests (kernel-assigned port).
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bind explicitly so a port-zero allocation surfaces before the goroutine
	// starts serving — easier failure mode to debug than ListenAndServe inside
	// the goroutine.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		// Drain Serve's return so the goroutine exits.
		<-errCh
		return nil
	}
}
