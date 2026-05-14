package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// api_key_auth_test.go covers T6: inbound auth path for lgw_-prefixed API
// keys. The middleware must:
//   - Accept a valid (unexpired, unrevoked) lgw_ token
//   - Reject revoked keys immediately (even if still in cache via explicit evict)
//   - Reject wrong-secret keys (bcrypt mismatch) with 401
//   - Accept the legacy LLM_GATEWAY_TOKEN when a store is also present
//   - Populate the request context with the verified APIKey (for T10 quota)
//   - Use the cache on second presentation (bcrypt runs but DB is skipped)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newAuthWithStore(t *testing.T, legacy string, st *store.Store) (*Auth, *auth.KeyCache) {
	t.Helper()
	cache := auth.NewKeyCache(time.Minute)
	a := NewAuthWithStore(legacy, st, cache)
	return a, cache
}

func TestAPIKeyAuth_ValidKey_Passes(t *testing.T) {
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	_, plaintext, err := st.IssueAPIKey(tm.ID, "inbound", "ci-bot")
	if err != nil {
		t.Fatal(err)
	}

	a, _ := newAuthWithStore(t, "", st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)

	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid API key should pass, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAPIKeyAuth_ValidKey_XApiKey_Passes(t *testing.T) {
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	_, plaintext, _ := st.IssueAPIKey(tm.ID, "inbound", "")

	a, _ := newAuthWithStore(t, "", st)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", plaintext)

	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestAPIKeyAuth_WrongSecret_Rejected(t *testing.T) {
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	ak, _, _ := st.IssueAPIKey(tm.ID, "inbound", "")

	a, _ := newAuthWithStore(t, "", st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	// same prefix, mangled secret
	req.Header.Set("Authorization", "Bearer "+ak.Prefix+"wrongsecrettail000000000000000x")

	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret should be 401, got %d", w.Code)
	}
}

func TestAPIKeyAuth_RevokedKey_Rejected(t *testing.T) {
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	ak, plaintext, _ := st.IssueAPIKey(tm.ID, "inbound", "")

	a, cache := newAuthWithStore(t, "", st)

	// Prime the cache with a valid first request.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first request should pass: %d", w.Code)
	}

	// Revoke and evict from cache (simulates MCP revoke_api_key tool).
	_ = st.RevokeAPIKey(ak.ID)
	cache.Evict(ak.Prefix)

	// Second request must be rejected.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("Authorization", "Bearer "+plaintext)
	w2 := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key should be 401, got %d", w2.Code)
	}
}

func TestAPIKeyAuth_LegacyToken_StillPassesWithStore(t *testing.T) {
	st := openTestStore(t)
	_, _ = st.AddTeam("acme", "Acme")

	a, _ := newAuthWithStore(t, "my-legacy-token", st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer my-legacy-token")

	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("legacy token should still work, got %d", w.Code)
	}
}

func TestAPIKeyAuth_CacheHit_SecondRequest_Passes(t *testing.T) {
	// First request primes the cache; second request hits cache and avoids DB.
	// Both should return 200 (we don't have a way to assert "no DB hit" in a
	// unit test, but we verify the cached path doesn't corrupt behavior).
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	_, plaintext, _ := st.IssueAPIKey(tm.ID, "inbound", "")

	a, _ := newAuthWithStore(t, "", st)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i+1, w.Code)
		}
	}
}

func TestAPIKeyAuth_ContextContainsAPIKey(t *testing.T) {
	// After successful API key auth, the request context must carry the
	// verified APIKey so quota middleware (T10) can read it without re-doing
	// auth.
	st := openTestStore(t)
	tm, _ := st.AddTeam("acme", "Acme")
	want, plaintext, _ := st.IssueAPIKey(tm.ID, "inbound", "ctx-test")

	a, _ := newAuthWithStore(t, "", st)

	var gotKey store.APIKey
	var gotOK bool
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotKey, gotOK = auth.APIKeyFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	a.Middleware(probe).ServeHTTP(w, req)

	if !gotOK {
		t.Fatal("APIKey not found in context after successful auth")
	}
	if gotKey.ID != want.ID {
		t.Errorf("context key mismatch: want %q, got %q", want.ID, gotKey.ID)
	}
}

func TestAPIKeyAuth_UnknownPrefix_Rejected(t *testing.T) {
	st := openTestStore(t)
	a, _ := newAuthWithStore(t, "", st)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer lgw_noprefixmatch0000zzzzzzzzzzzzzzzzz")
	w := httptest.NewRecorder()
	a.Middleware(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown prefix should be 401, got %d", w.Code)
	}
}
