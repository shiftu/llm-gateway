package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// auth_test.go covers the bearer-auth middleware. Per plan §1 P3 the gateway
// accepts BOTH OpenAI-style `Authorization: Bearer <token>` and Anthropic-
// style `x-api-key: <token>` inbound — matching the convention each protocol's
// clients send natively. Per F-DX-05 every error body must include
// `{error:{type,message,fix}}` so callers can self-correct.

func newTestAuth(t *testing.T) *Auth {
	t.Helper()
	return NewAuth("valid-token")
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func parseErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response missing top-level `error` object: %v", body)
	}
	for _, k := range []string{"type", "message", "fix"} {
		if v, ok := errObj[k]; !ok || v == "" {
			t.Fatalf("error.%s missing or empty per F-DX-05: %v", k, errObj)
		}
	}
	return errObj
}

func TestAuth_MissingCredentials_Returns401(t *testing.T) {
	auth := newTestAuth(t)
	h := auth.Middleware(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", w.Code, w.Body.String())
	}
	errObj := parseErrorBody(t, w)
	if errObj["type"] != "missing_credentials" {
		t.Fatalf("want error.type=missing_credentials, got %v", errObj["type"])
	}
}

func TestAuth_WrongBearer_Returns401(t *testing.T) {
	auth := newTestAuth(t)
	h := auth.Middleware(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	errObj := parseErrorBody(t, w)
	if errObj["type"] != "invalid_credentials" {
		t.Fatalf("want error.type=invalid_credentials, got %v", errObj["type"])
	}
}

func TestAuth_ValidBearer_Passes(t *testing.T) {
	auth := newTestAuth(t)
	h := auth.Middleware(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (passed through), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestAuth_ValidXApiKey_Passes ensures Anthropic-style `x-api-key` header is
// accepted equivalently to OpenAI Bearer. Per plan R2: both inbound protocols
// must work without forcing callers to know which header the gateway prefers.
func TestAuth_ValidXApiKey_Passes(t *testing.T) {
	auth := newTestAuth(t)
	h := auth.Middleware(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "valid-token")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestAuth_AuthorizationHeader_MalformedScheme treats "Authorization: SomethingElse foo"
// as missing credentials (not invalid). This avoids leaking what the gateway
// expects through differential error messages.
func TestAuth_AuthorizationHeader_MalformedScheme(t *testing.T) {
	auth := newTestAuth(t)
	h := auth.Middleware(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	// Confirm "fix" field actually contains the words "Bearer" or "x-api-key"
	// so a caller can self-correct.
	errObj := parseErrorBody(t, w)
	fix := errObj["fix"].(string)
	if !strings.Contains(fix, "Bearer") && !strings.Contains(fix, "x-api-key") {
		t.Fatalf("error.fix must mention Bearer or x-api-key to be actionable; got %q", fix)
	}
}
