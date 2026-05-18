package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/provider"
	"github.com/panda/llm-gateway/internal/router"
	"github.com/panda/llm-gateway/internal/store"
)

// Server composes auth middleware + inbound mux + (optional) store-backed
// routing. When store is nil the gateway runs in stub mode for offline dev
// (Task 1 behaviour preserved). With a store, each request is peeked for
// its `model` field, routed by the router, optionally rewritten if an alias
// maps to a different upstream model, dispatched via the provider's
// matching base_url (pass-through R2 fast path), and logged.
//
// Cross-protocol calls (e.g. Anthropic inbound + provider-only-OpenAI)
// currently return 501 until Task 2b IR translation lands.
type Server struct {
	auth    *Auth
	store   *store.Store
	router  *router.Router
	quota   *QuotaMW
	mux     *http.ServeMux
	monitor *monitoring
}

// NewServer wires auth + (optional) store-driven routing. Pass nil for
// store to keep stub-mode handlers (offline dev).
//
// When store is non-nil, NewAuthWithStore is used so that lgw_-prefixed API
// keys are accepted alongside the legacy token (T6 inbound auth path). The
// KeyCache uses a 60-second TTL matching plan R3 FINAL.
func NewServer(token string, s *store.Store) *Server {
	var a *Auth
	if s != nil {
		cache := authpkg.NewKeyCache(60 * time.Second)
		a = NewAuthWithStore(token, s, cache)
	} else {
		a = NewAuth(token)
	}
	srv := &Server{
		auth:  a,
		store: s,
		mux:   http.NewServeMux(),
	}
	if s != nil {
		srv.router = router.New(s)
		srv.quota = NewQuotaMW(s)
	}
	srv.mux.HandleFunc("/v1/chat/completions", srv.dispatchOpenAI)
	srv.mux.HandleFunc("/v1/messages", srv.dispatchAnthropic)
	srv.mux.HandleFunc("/v1/models", srv.handleModels)
	srv.mountMonitoring()
	return srv
}

// MountMCP wires the Streamable HTTP MCP transport onto /mcp on the shared
// mux. The handler is expected to be BuildMCPHandler's output: legacyKeyShim
// wrapping mcp-go's StreamableHTTPServer, WITHOUT its own auth middleware.
// /mcp travels through Server.Handler()'s outer auth.Middleware exactly like
// /v1/* and is skipped by quota.Middleware via path check (see quota.go).
//
// Skips registration if h is nil so the gateway boots cleanly when MCP is
// disabled.
func (s *Server) MountMCP(h http.Handler) {
	if h == nil {
		return
	}
	s.mux.Handle("/mcp", h)
}

// MarkReady flips the /healthz readiness gate (Q11). Call after
// Manager.Bootstrap completes — until then /healthz returns 503.
func (s *Server) MarkReady() {
	s.monitor.MarkReady()
}

func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	if s.quota != nil {
		h = s.quota.Middleware(h)
	}
	return s.auth.Middleware(h)
}

const (
	protocolOpenAI    = "openai"
	protocolAnthropic = "anthropic"
)

func (s *Server) dispatchOpenAI(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		HandleChatCompletions(w, r)
		return
	}
	s.routeAndForward(w, r, protocolOpenAI)
}

func (s *Server) dispatchAnthropic(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		HandleMessages(w, r)
		return
	}
	s.routeAndForward(w, r, protocolAnthropic)
}

// routeAndForward implements the store-backed request lifecycle.
// Order: read body → extract model → router.Resolve → maybe-rewrite-model
// → choose provider base_url matching `protocol` → forward → log.
//
// Byte fidelity: if router resolution returns the same model name (default
// route, no alias rewrite), the original body bytes are forwarded verbatim.
// Only when an alias remaps the model does the body get re-encoded.
func (s *Server) routeAndForward(w http.ResponseWriter, r *http.Request, protocol string) {
	s.monitor.IncRequests()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeStructuredError(w, http.StatusBadRequest, "body_read_error", err.Error(),
			"resend the request; if it persists, check client-side body handling")
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeStructuredError(w, http.StatusBadRequest, "invalid_json", err.Error(),
			"send a JSON body matching OpenAI Chat Completions or Anthropic Messages shape")
		return
	}
	clientModel, _ := body["model"].(string)
	if clientModel == "" {
		writeStructuredError(w, http.StatusBadRequest, "missing_model",
			"request body must include a non-empty 'model' field", "set 'model' to a registered alias or upstream model name")
		return
	}

	ak, hasKey := authpkg.APIKeyFromContext(r.Context())
	teamID := ""
	if hasKey {
		teamID = ak.TeamID
	}

	route, err := s.router.ResolveForTeam(clientModel, teamID)
	if err != nil {
		writeStructuredError(w, http.StatusNotFound, "no_route", err.Error(),
			"register a model alias via MCP set_model_alias, or set a default provider via set_default_provider")
		return
	}

	bodyToSend := io.Reader(bytes.NewReader(raw))
	if route.UpstreamModel != clientModel {
		body["model"] = route.UpstreamModel
		rewritten, _ := json.Marshal(body)
		bodyToSend = bytes.NewReader(rewritten)
	}

	// F-3 snapshot: copy provider fields needed for this single request so
	// concurrent MCP add_provider/remove_provider cannot mutate the row out
	// from under us mid-flight.
	p := &provider.Provider{
		Name:             route.Provider.Name,
		Kind:             route.Provider.Kind,
		OpenAIBaseURL:    route.Provider.OpenAIBaseURL,
		AnthropicBaseURL: route.Provider.AnthropicBaseURL,
		APIKey:           route.Provider.APIKey,
		AnthropicVersion: route.Provider.AnthropicVersion,
	}

	started := time.Now()
	resp, dispatchErr := dispatchProtocol(r.Context(), p, protocol, bodyToSend)
	if dispatchErr != nil {
		rl := store.RequestLog{
			ClientModel:  clientModel,
			ResolvedModel: route.UpstreamModel,
			ProviderName: route.Provider.Name,
			Status:       "upstream_error",
			LatencyMs:    int(time.Since(started).Milliseconds()),
			ErrorMsg:     dispatchErr.Error(),
		}
		if hasKey {
			rl.APIKeyID = ak.ID
			rl.TeamID = ak.TeamID
		}
		s.logRequest(rl)
		if errors.Is(dispatchErr, provider.ErrProtocolUnsupported) {
			writeStructuredError(w, http.StatusNotImplemented, "cross_protocol_not_supported",
				fmt.Sprintf("provider %q has no %s base_url; IR translation not yet implemented", p.Name, protocol),
				fmt.Sprintf("register the provider with %s_base_url set, or wait for Task 2b IR translation in v0.2", protocol))
			return
		}
		writeUpstreamError(w, dispatchErr)
		return
	}
	defer resp.Body.Close()

	u := pipeAndCaptureUsage(w, resp, protocol)

	rl := store.RequestLog{
		ClientModel:      clientModel,
		ResolvedModel:    route.UpstreamModel,
		ProviderName:     route.Provider.Name,
		Status:           "ok",
		LatencyMs:        int(time.Since(started).Milliseconds()),
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens + u.ReasoningTokens,
	}
	if hasKey {
		rl.APIKeyID = ak.ID
		rl.TeamID = ak.TeamID
		if s.store != nil {
			dayUTC := truncateToDay(time.Now().UTC())
			costMicros := calcCostMicros(s.store, route.Provider.Name, route.UpstreamModel, u)
			_ = s.store.CommitUsage(ak.ID, dayUTC,
				int64(u.InputTokens), int64(u.OutputTokens), int64(u.ReasoningTokens), costMicros)
		}
	}
	s.logRequest(rl)
}

func dispatchProtocol(ctx context.Context, p *provider.Provider, protocol string, body io.Reader) (*http.Response, error) {
	switch protocol {
	case protocolOpenAI:
		return p.OpenAIRequest(ctx, body)
	case protocolAnthropic:
		return p.AnthropicRequest(ctx, body)
	default:
		return nil, fmt.Errorf("unknown protocol %q", protocol)
	}
}

// logRequest writes a request_logs row; swallowing errors is acceptable for
// observability (we never want logging to fail a real request). v0.1 doesn't
// extract token counts from the upstream response — those land in Task 6+/v0.2
// when the response body is parsed for billing/usage.
func (s *Server) logRequest(r store.RequestLog) {
	if s.store == nil {
		return
	}
	if r.Ts.IsZero() {
		r.Ts = time.Now()
	}
	_, _ = s.store.LogRequest(r)
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
		<-errCh
		return nil
	}
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

// pipeAndCaptureUsage pipes the upstream response to the client while
// intercepting the body to extract token-usage counts. For streaming responses
// an io.TeeReader accumulates SSE bytes as they flow to the client. For
// blocking responses the body is read once, forwarded, then parsed.
// Caller retains responsibility for resp.Body.Close via defer.
func pipeAndCaptureUsage(w http.ResponseWriter, resp *http.Response, protocol string) capturedUsage {
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var buf bytes.Buffer
		flushAndCopy(w, io.TeeReader(resp.Body, &buf))
		return parseSSEUsage(buf.Bytes(), protocol)
	}
	body, _ := io.ReadAll(resp.Body)
	_, _ = w.Write(body)
	return parseBlockingUsage(body, protocol)
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
	writeStructuredError(w, http.StatusBadGateway, "upstream_error",
		fmt.Sprintf("failed to reach upstream: %v", err),
		"check provider base_url + network; tail recent request_logs for details")
}

func writeStructuredError(w http.ResponseWriter, status int, kind, msg, fix string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    kind,
			"message": msg,
			"fix":     fix,
		},
	})
}
