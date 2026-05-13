// Package provider issues upstream HTTP calls to LLM providers (DeepSeek,
// GLM, ...). Two access modes per plan R2:
//
//   - Pass-through: provider exposes the inbound protocol natively — bytes
//     are forwarded verbatim (URL + headers swapped, body untouched). Used
//     when client and provider speak the same protocol. Cheapest, fastest,
//     no IR allocation. Task 2a (this file).
//
//   - IR translate: provider only supports the *other* protocol. Caller
//     converts inbound JSON → ir.Request, this package converts ir.Request →
//     upstream native shape, then converts upstream response → ir.Response.
//     Task 2b (CompleteIR / StreamIR — not in this commit).
//
// Provider is a struct rather than an interface because every known provider
// (DeepSeek, GLM, OpenAI, Anthropic) shares the same shape: a name + a kind
// + up to two base URLs + an API key. If per-kind quirks emerge (e.g. GLM
// JWT-style auth), we switch on Kind inside the methods rather than
// fragmenting into types.
package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// DefaultAnthropicVersion is the version string sent in the
// `anthropic-version` header when a provider entry doesn't specify one.
// Matches the version DeepSeek's anthropic endpoint expects (spike F4).
const DefaultAnthropicVersion = "2023-06-01"

// ErrProtocolUnsupported is returned when the caller requests a protocol the
// provider has no base URL for — e.g. AnthropicRequest on a provider with
// only OpenAIBaseURL set. The router must fall back to IR translation in
// that case (Task 5 + Task 2b).
var ErrProtocolUnsupported = errors.New("provider does not support this inbound protocol natively")

// Provider holds the static config needed to call one upstream provider.
// Loaded from SQLite providers table per plan R2 schema. Two base URLs
// because providers like DeepSeek serve both protocols on different paths.
type Provider struct {
	Name             string // e.g. "deepseek", "glm-prod"
	Kind             string // e.g. "deepseek", "glm"
	OpenAIBaseURL    string // empty if provider has no OpenAI-compat endpoint
	AnthropicBaseURL string // empty if provider has no Anthropic-compat endpoint
	APIKey           string
	AnthropicVersion string // optional; defaults to DefaultAnthropicVersion

	// HTTPClient is exposed for tests to inject a mock transport. nil falls
	// back to defaultClient.
	HTTPClient *http.Client
}

// defaultClient is shared across all providers. The long timeout matters for
// reasoning models like DeepSeek v4-flash that can spend tens of seconds in
// hidden chain-of-thought before emitting visible content.
var defaultClient = &http.Client{
	Timeout: 5 * time.Minute,
}

func (p *Provider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return defaultClient
}

// OpenAIRequest forwards body verbatim to the provider's OpenAI-compat
// endpoint and returns the upstream response. Caller owns resp.Body.Close.
//
// Spike F-Q16 (byte fidelity): body bytes are passed straight through;
// no JSON re-encoding. The handler in internal/server is responsible for
// piping resp.Body back to the inbound ResponseWriter with the upstream's
// Content-Type (which may be `application/json` for blocking or
// `text/event-stream` for streaming — both are handled the same way here).
func (p *Provider) OpenAIRequest(ctx context.Context, body io.Reader) (*http.Response, error) {
	if p.OpenAIBaseURL == "" {
		return nil, ErrProtocolUnsupported
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.OpenAIBaseURL+"/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return p.client().Do(req)
}

// AnthropicRequest forwards body verbatim to the provider's Anthropic-compat
// endpoint. Anthropic convention uses `x-api-key` + `anthropic-version`,
// not `Authorization: Bearer` (spike F4). Headers are set accordingly.
func (p *Provider) AnthropicRequest(ctx context.Context, body io.Reader) (*http.Response, error) {
	if p.AnthropicBaseURL == "" {
		return nil, ErrProtocolUnsupported
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.AnthropicBaseURL+"/v1/messages", body)
	if err != nil {
		return nil, err
	}
	version := p.AnthropicVersion
	if version == "" {
		version = DefaultAnthropicVersion
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", version)
	req.Header.Set("Content-Type", "application/json")
	return p.client().Do(req)
}
