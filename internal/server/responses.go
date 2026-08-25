// codex（0.137.0+）只认 Responses API，网关只有 Chat Completions —— 见
// docs/design/responses-api.md。这个文件是那份契约的服务端落地：入站翻成
// Chat、永远走 protocolOpenAI 转发、上游 Chat SSE 再翻回 Responses SSE。
//
// 不复用 routeAndForward：那条路径假设入站协议和转发协议是同一件事
// （逐字节转发或者 501）。Responses 两头协议永远不同，且永远流式，
// 翻译逻辑和 pipeAndCaptureUsage 的字节转发模型对不上。
package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/ir"
	"github.com/panda/llm-gateway/internal/provider"
	"github.com/panda/llm-gateway/internal/router"
	"github.com/panda/llm-gateway/internal/store"
)

func (s *Server) dispatchResponses(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		writeStructuredError(w, http.StatusServiceUnavailable, "responses_unavailable",
			"gateway is running in stub mode (no store configured)",
			"configure a store-backed gateway to use /v1/responses")
		return
	}
	s.monitor.IncRequests()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeStructuredError(w, http.StatusBadRequest, "body_read_error", err.Error(),
			"resend the request; if it persists, check client-side body handling")
		return
	}

	var top struct {
		Model string `json:"model"`
	}
	if jerr := json.Unmarshal(raw, &top); jerr != nil {
		writeStructuredError(w, http.StatusBadRequest, "invalid_json", jerr.Error(),
			"send a JSON body matching the Responses API shape")
		return
	}
	clientModel := top.Model
	if clientModel == "" {
		writeStructuredError(w, http.StatusBadRequest, "missing_model",
			"request body must include a non-empty 'model' field", "set 'model' to a registered alias or upstream model name")
		return
	}

	chatBody, err := ir.ChatRequestFromResponses(raw)
	if err != nil {
		writeStructuredError(w, http.StatusBadRequest, "invalid_responses_input", err.Error(),
			"input[] must contain at least one message/function_call/function_call_output item that produces content")
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

	if route.UpstreamModel != clientModel {
		var body map[string]any
		_ = json.Unmarshal(chatBody, &body)
		body["model"] = route.UpstreamModel
		chatBody, _ = json.Marshal(body)
	}

	// F-3 snapshot, same rationale as routeAndForward: freeze the provider
	// fields this request uses so a concurrent MCP add_provider/remove_provider
	// can't mutate them out from under an in-flight stream.
	currentProvider := &provider.Provider{
		Name:             route.Provider.Name,
		Kind:             route.Provider.Kind,
		OpenAIBaseURL:    route.Provider.OpenAIBaseURL,
		AnthropicBaseURL: route.Provider.AnthropicBaseURL,
		APIKey:           route.Provider.APIKey,
		AnthropicVersion: route.Provider.AnthropicVersion,
	}

	started := time.Now()
	// Always OpenAI: the translated body is Chat-shaped regardless of what
	// codex originally sent. A provider with no openai_base_url can't serve
	// this route at all.
	resp, dispatchErr := currentProvider.OpenAIRequest(r.Context(), bytes.NewReader(chatBody))
	if dispatchErr != nil {
		s.logResponsesFailure(clientModel, route, currentProvider, started, dispatchErr.Error(), hasKey, ak)
		if errors.Is(dispatchErr, provider.ErrProtocolUnsupported) {
			writeStructuredError(w, http.StatusNotImplemented, "cross_protocol_not_supported",
				fmt.Sprintf("provider %q has no openai base_url; /v1/responses always dispatches via Chat Completions", currentProvider.Name),
				"register the provider with openai_base_url set")
			return
		}
		writeUpstreamError(w, dispatchErr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Upstream rejected before any SSE commitment — pipe its error body
		// through untranslated. The Responses SSE contract (response.completed
		// must always arrive) only applies once we've committed to a 200 stream.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(body)
		s.logResponsesFailure(clientModel, route, currentProvider, started,
			fmt.Sprintf("upstream status %d", resp.StatusCode), hasKey, ak)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeFrames := func(frames [][]byte) {
		for _, f := range frames {
			_, _ = w.Write(f)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	respID := "resp_" + randomHex(16)
	state := ir.NewResponsesStreamState(respID, route.UpstreamModel)
	writeFrames(state.Start())

	// Upstream bytes here are genuine Chat Completions SSE (we dispatched via
	// protocolOpenAI above), so the existing OpenAI usage parser applies
	// directly to the pre-translation chunks — no need for a parallel
	// "protocolResponses" branch in usage.go.
	var upstreamUsage capturedUsage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		applySSEChunkUsage(&upstreamUsage, []byte(data), protocolOpenAI)
		writeFrames(state.Feed([]byte(data)))
	}
	writeFrames(state.Finish())

	rl := store.RequestLog{
		ClientModel:      clientModel,
		ResolvedModel:    route.UpstreamModel,
		ProviderName:     currentProvider.Name,
		Status:           "ok",
		LatencyMs:        int(time.Since(started).Milliseconds()),
		PromptTokens:     upstreamUsage.InputTokens,
		CompletionTokens: upstreamUsage.OutputTokens,
		TotalTokens:      upstreamUsage.InputTokens + upstreamUsage.OutputTokens + upstreamUsage.ReasoningTokens,
		CachedTokens:     upstreamUsage.CachedTokens,
		RouteTrace:       marshalCognitiveTrace(route.Cognitive),
	}
	if hasKey {
		rl.APIKeyID = ak.ID
		rl.TeamID = ak.TeamID
		if s.store != nil {
			dayUTC := truncateToDay(time.Now().UTC())
			costMicros := calcCostMicros(s.store, route.Provider.Name, route.UpstreamModel, upstreamUsage)
			_ = s.store.CommitUsage(ak.ID, dayUTC,
				int64(upstreamUsage.InputTokens), int64(upstreamUsage.OutputTokens), int64(upstreamUsage.ReasoningTokens),
				int64(upstreamUsage.CachedTokens), costMicros)
		}
	}
	s.logRequest(rl)
}

func (s *Server) logResponsesFailure(clientModel string, route router.Route, p *provider.Provider, started time.Time, errMsg string, hasKey bool, ak store.APIKey) {
	rl := store.RequestLog{
		ClientModel:   clientModel,
		ResolvedModel: route.UpstreamModel,
		ProviderName:  p.Name,
		Status:        "upstream_error",
		LatencyMs:     int(time.Since(started).Milliseconds()),
		ErrorMsg:      errMsg,
	}
	if hasKey {
		rl.APIKeyID = ak.ID
		rl.TeamID = ak.TeamID
	}
	s.logRequest(rl)
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
