package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Server composes the bearer-auth middleware with the inbound mux. v0.1
// scope: 2 routes (OpenAI + Anthropic). Future revisions add more routes
// without changing this assembly point.
type Server struct {
	auth *Auth
	mux  *http.ServeMux
}

// NewServer builds the mux and wires both inbound endpoints.
func NewServer(token string) *Server {
	s := &Server{
		auth: NewAuth(token),
		mux:  http.NewServeMux(),
	}
	s.mux.HandleFunc("/v1/chat/completions", HandleChatCompletions)
	s.mux.HandleFunc("/v1/messages", HandleMessages)
	return s
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
