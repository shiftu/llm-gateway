package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// Auth holds the bearer-token middleware state. Supports two auth paths:
//
//  1. Legacy LLM_GATEWAY_TOKEN — single shared secret kept permanently as the
//     bootstrap + lost-key recovery mechanism (plan R3 UC-R3-5=A). Fast O(1)
//     compare; no DB round-trip.
//
//  2. lgw_-prefixed API keys — issued by IssueAPIKey, stored bcrypt-hashed in
//     api_keys table. The 60-second TTL cache (T6) means the DB lookup only
//     pays its cost once per minute per active key; subsequent requests skip
//     the DB and run bcrypt in-process against the cached hash.
//
// Both paths accept OpenAI-style `Authorization: Bearer` AND Anthropic-style
// `x-api-key` so callers don't need to know which header convention the
// gateway prefers (plan §1 P3).
type Auth struct {
	legacyToken string
	store       *store.Store
	cache       *authpkg.KeyCache
}

// NewAuth constructs the middleware for legacy-token-only mode (no store).
// Preserved for compatibility with existing tests and stub-mode startup.
func NewAuth(token string) *Auth {
	return &Auth{legacyToken: token}
}

// NewAuthWithStore constructs the middleware with both a legacy token and a
// store-backed API key path. Pass an empty string for legacyToken to disable
// legacy auth entirely.
func NewAuthWithStore(legacyToken string, st *store.Store, cache *authpkg.KeyCache) *Auth {
	return &Auth{legacyToken: legacyToken, store: st, cache: cache}
}

// Middleware wraps next with inbound auth. Decision order:
//  1. Missing credentials → 401 missing_credentials
//  2. Matches legacyToken (when set) → pass (fast path)
//  3. Starts with "lgw_" and store present → API key path (cache + bcrypt)
//  4. Otherwise → 401 invalid_credentials
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := extractCredentials(r)
		if !ok {
			writeAuthError(w, "missing_credentials",
				"no Authorization or x-api-key header found",
				"set the gateway's token via LLM_GATEWAY_TOKEN and send it as 'Authorization: Bearer <token>' (OpenAI clients) or 'x-api-key: <token>' (Anthropic clients)")
			return
		}

		// Fast path: legacy shared token.
		if a.legacyToken != "" && provided == a.legacyToken {
			next.ServeHTTP(w, r)
			return
		}

		// API key path: lgw_<16-char-prefix><32-char-secret>
		if a.store != nil && strings.HasPrefix(provided, "lgw_") && len(provided) >= 20 {
			a.handleAPIKey(w, r, provided, next)
			return
		}

		writeAuthError(w, "invalid_credentials",
			"token does not match the configured LLM_GATEWAY_TOKEN",
			"verify the token you sent matches the gateway's LLM_GATEWAY_TOKEN env var (rotate via 'llm-gateway init' if lost)")
	})
}

// handleAPIKey implements the cache-then-bcrypt auth sub-path for lgw_ tokens.
func (a *Auth) handleAPIKey(w http.ResponseWriter, r *http.Request, plaintext string, next http.Handler) {
	prefix := plaintext[:20]

	// Try cache first to avoid a DB round-trip on every request.
	ak, cached := a.cache.Get(prefix)
	if !cached {
		row, err := a.store.LookupAPIKeyByPrefix(prefix)
		if errors.Is(err, store.ErrNotFound) {
			writeAuthError(w, "invalid_credentials",
				"API key not found or has been revoked",
				"check the token matches a key issued by issue_api_key; revoked or expired keys must be reissued")
			return
		}
		if err != nil {
			writeAuthError(w, "internal_error", "auth lookup failed", "retry; if it persists check gateway logs")
			return
		}
		ak = row
	}

	// Always bcrypt-compare regardless of cache state — a token with a known
	// prefix but wrong secret must be rejected even when the prefix is cached.
	if err := bcrypt.CompareHashAndPassword([]byte(ak.Hash), []byte(plaintext)); err != nil {
		writeAuthError(w, "invalid_credentials",
			"API key secret does not match",
			"check you are sending the original plaintext token returned at issuance")
		return
	}

	// Auth succeeded: warm the cache and thread the key into the request context.
	a.cache.Put(ak)
	next.ServeHTTP(w, r.WithContext(authpkg.NewContext(r.Context(), ak)))
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
