package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Auth holds the bearer-token middleware state. Single token shared by both
// inbound protocols per plan §1 P3 — OpenAI Authorization-Bearer and
// Anthropic x-api-key are accepted interchangeably so callers don't need to
// know which header convention the gateway prefers.
type Auth struct {
	token string
}

// NewAuth constructs the middleware with a single valid token. In v0.1 this
// token comes from the LLM_GATEWAY_TOKEN env var via start.go (Task 11).
// Empty-token construction is reserved for future "dev-mode" wiring per
// plan DX-F11; v0.1 production startup must enforce a non-empty token.
func NewAuth(token string) *Auth {
	return &Auth{token: token}
}

// Middleware wraps next, returning 401 with a structured error body per
// F-DX-05 when credentials are missing or wrong. The error payload format
// is `{error:{type,message,fix}}` — uniform across both inbound protocols.
// Per-protocol error shaping (OpenAI vs Anthropic conventions) deferred.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := extractCredentials(r)
		if !ok {
			writeAuthError(w, "missing_credentials",
				"no Authorization or x-api-key header found",
				"set the gateway's token via LLM_GATEWAY_TOKEN and send it as 'Authorization: Bearer <token>' (OpenAI clients) or 'x-api-key: <token>' (Anthropic clients)")
			return
		}
		if provided != a.token {
			writeAuthError(w, "invalid_credentials",
				"token does not match the configured LLM_GATEWAY_TOKEN",
				"verify the token you sent matches the gateway's LLM_GATEWAY_TOKEN env var (rotate via 'llm-gateway init' if lost)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractCredentials pulls the inbound token from either the OpenAI-style
// Authorization Bearer header or the Anthropic-style x-api-key header.
// Returns ("", false) if neither is present in a recognised form — including
// the case where Authorization is set but uses a non-Bearer scheme.
func extractCredentials(r *http.Request) (string, bool) {
	if v := r.Header.Get("Authorization"); v != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(v, prefix) {
			tok := strings.TrimSpace(v[len(prefix):])
			if tok != "" {
				return tok, true
			}
		}
		return "", false
	}
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v, true
	}
	return "", false
}

func writeAuthError(w http.ResponseWriter, kind, msg, fix string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    kind,
			"message": msg,
			"fix":     fix,
		},
	})
}
