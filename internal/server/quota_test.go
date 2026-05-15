package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// openQuotaStore opens a fresh in-memory store and registers cleanup.
func openQuotaStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// issueTestKey creates a team + API key and returns the APIKey row.
func issueTestKey(t *testing.T, st *store.Store, slug string) store.APIKey {
	t.Helper()
	team, err := st.AddTeam(slug, slug)
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := st.IssueAPIKey(team.ID, "inbound", "test")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	return ak
}

// quotaRequest builds a POST to /v1/chat/completions with the given APIKey
// injected into the context (simulating auth middleware having run).
func quotaRequest(ak store.APIKey) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4"}`))
	r = r.WithContext(authpkg.NewContext(r.Context(), ak))
	return r
}

// passHandler is a trivial next handler that writes 200.
var passHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// --- legacy bypass ---

func TestQuotaMW_LegacyToken_Bypasses(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)

	// Set an absurdly low day quota that would trip on any real key.
	n := int64(0)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: "__legacy__", Window: "day", MaxRequests: &n})

	legacyKey := store.APIKey{ID: "__legacy__"}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(authpkg.NewContext(r.Context(), legacyKey))

	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("legacy token should bypass quota, got %d", w.Code)
	}
}

// --- no quota configured ---

func TestQuotaMW_NoQuota_PassesThrough(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusOK {
		t.Errorf("want 200 when no quota set, got %d", w.Code)
	}
}

// --- no APIKey in context (should pass through gracefully) ---

func TestQuotaMW_NoKeyInContext_PassesThrough(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("want 200 when no key in context, got %d", w.Code)
	}
}

// --- day quota ---

func TestQuotaMW_DayQuota_Enforced(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(2)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "day", MaxRequests: &limit})

	// Seed 2 requests already counted for today.
	dayUTC := truncateToDay(time.Now().UTC())
	_, _ = st.ReserveQuota(ak.ID, dayUTC, 2)

	// Third request should be rejected.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429, got %d", w.Code)
	}
}

func TestQuotaMW_DayQuota_NotExceeded_PassesThrough(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(10)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "day", MaxRequests: &limit})

	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}

func TestQuotaMW_DayQuota_Rollback_OnExceed(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(1)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "day", MaxRequests: &limit})

	// Seed 1 request (at limit).
	dayUTC := truncateToDay(time.Now().UTC())
	_, _ = st.ReserveQuota(ak.ID, dayUTC, 1)

	// This request should be rejected AND the counter rolled back.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429, got %d", w.Code)
	}

	// Counter should still be 1 after rollback (not 2).
	c, err := st.GetUsageCounter(ak.ID, dayUTC)
	if err != nil {
		t.Fatalf("GetUsageCounter: %v", err)
	}
	if c.RequestCount != 1 {
		t.Errorf("counter should be 1 after rollback, got %d", c.RequestCount)
	}
}

// --- team-scope quota ---

func TestQuotaMW_TeamQuota_Enforced(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(0)
	_ = st.SetQuota(store.Quota{ScopeKind: "team", ScopeID: ak.TeamID, Window: "day", MaxRequests: &limit})

	// Key-scope quota is absent; team-scope applies.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429 from team quota, got %d", w.Code)
	}
}

// --- minute RPM ---

func TestQuotaMW_MinuteRPM_Enforced(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(2)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "minute", MaxRequests: &limit})

	// First two requests should pass.
	for i := range 2 {
		w := httptest.NewRecorder()
		q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
		if w.Code != http.StatusOK {
			t.Errorf("request %d: want 200, got %d", i+1, w.Code)
		}
	}

	// Third request in the same minute should be rejected.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429 on RPM exceed, got %d", w.Code)
	}
}

func TestQuotaMW_MinuteRPM_ResetsAfterWindow(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(1)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "minute", MaxRequests: &limit})

	// Consume the one allowed request.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusOK {
		t.Fatalf("first request should pass, got %d", w.Code)
	}

	// Manually expire the window by backdating the slot.
	q.mu.Lock()
	if slot, ok := q.rpm[ak.ID]; ok {
		slot.windowEnd = time.Now().Add(-time.Second)
	}
	q.mu.Unlock()

	// After window reset, next request should pass again.
	w = httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusOK {
		t.Errorf("want 200 after RPM window reset, got %d", w.Code)
	}
}

// --- month quota ---

func TestQuotaMW_MonthQuota_Enforced(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	ak := issueTestKey(t, st, "acme")

	limit := int64(5)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "month", MaxRequests: &limit})

	// Seed 5 requests spread across days this month.
	now := time.Now().UTC()
	dayUTC := truncateToDay(now)
	_, _ = st.ReserveQuota(ak.ID, dayUTC, 5)

	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429 on month quota exceed, got %d", w.Code)
	}
}

// --- SQLite-backed RPM persistence (LLM_GATEWAY_PERSIST_RPM=1) ---

func TestQuotaMW_PersistRPM_Enforced(t *testing.T) {
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	q.persistRPM = true // enable SQLite-backed RPM

	ak := issueTestKey(t, st, "persist-rpm")

	limit := int64(2)
	_ = st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "minute", MaxRequests: &limit})

	// Two requests should pass.
	for i := range 2 {
		w := httptest.NewRecorder()
		q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
		if w.Code != http.StatusOK {
			t.Errorf("request %d: want 200, got %d", i+1, w.Code)
		}
	}

	// Third request should be rejected.
	w := httptest.NewRecorder()
	q.Middleware(passHandler).ServeHTTP(w, quotaRequest(ak))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("want 429 on RPM exceed (persist mode), got %d", w.Code)
	}
}

func TestQuotaMW_PersistRPM_DBErrorGracelfullyHandled(t *testing.T) {
	// When persistRPM=true and the DB is closed, GetAndIncrementRPM fails.
	// The middleware falls back to in-memory RPM tracking for that key.
	// Since findQuota also fails (DB closed), no quota is found → pass-through.
	// This is acceptable: a closed DB means no quota enforcement at all,
	// consistent with how day/month quotas also silently pass through on DB errors.
	st := openQuotaStore(t)
	q := NewQuotaMW(st)
	q.persistRPM = true

	fakeKey := store.APIKey{ID: "fake-key-dberr", TeamID: ""}
	_ = st.Close()

	// All requests pass through when DB is closed — no quota config loaded.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	r = r.WithContext(authpkg.NewContext(r.Context(), fakeKey))
	q.Middleware(passHandler).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("closed DB: want 200 (no quota loaded), got %d", w.Code)
	}
}
