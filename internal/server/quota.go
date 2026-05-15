package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// QuotaMW enforces per-request quotas after auth.
//
// Check order (fail-fast, cheapest first):
//  1. Minute RPM bucket (in-memory by default; SQLite-backed when
//     LLM_GATEWAY_PERSIST_RPM=1 for crash-safe persistence)
//  2. Month request cap (best-effort SQLite SUM — not atomic)
//  3. Day request cap (atomic SQLite UPDATE-RETURNING via ReserveQuota;
//     over-limit triggers a compensating −1 decrement)
//
// Quota is looked up first by key scope ("key", ak.ID), then by team scope
// ("team", ak.TeamID). If neither is configured for a window → unlimited.
// Legacy __legacy__ tokens bypass all quota checks.
type QuotaMW struct {
	st         *store.Store
	mu         sync.Mutex
	rpm        map[string]*rpmSlot // key: api_key.ID (only used when persistRPM=false)
	persistRPM bool                // true when LLM_GATEWAY_PERSIST_RPM=1
}

type rpmSlot struct {
	count     int64
	windowEnd time.Time
}

// NewQuotaMW creates a new quota middleware backed by the given store.
// Reads LLM_GATEWAY_PERSIST_RPM env on creation; value is immutable after.
func NewQuotaMW(st *store.Store) *QuotaMW {
	persist := os.Getenv("LLM_GATEWAY_PERSIST_RPM") == "1"
	return &QuotaMW{st: st, rpm: make(map[string]*rpmSlot), persistRPM: persist}
}

// Middleware returns an http.Handler that enforces quotas before calling next.
func (q *QuotaMW) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /mcp is admin/control plane (JSON-RPC), not inbound LLM traffic;
		// counting it against an lgw_ key's RPM/day budget would conflate
		// usage with administration. Skip quota entirely on this path.
		if r.URL.Path == "/mcp" {
			next.ServeHTTP(w, r)
			return
		}
		ak, ok := authpkg.APIKeyFromContext(r.Context())
		if !ok || ak.ID == "__legacy__" {
			next.ServeHTTP(w, r)
			return
		}

		now := time.Now().UTC()

		// 1. Minute RPM.
		if quota, found := q.findQuota(ak, "minute"); found && quota.MaxRequests != nil {
			if exceeded := q.checkRPM(ak.ID, *quota.MaxRequests, now); exceeded {
				writeQuotaError(w, "minute", *quota.MaxRequests)
				return
			}
		}

		// 2. Month (best-effort pre-check, non-atomic).
		monthStart := truncateToMonth(now)
		tomorrowUTC := truncateToDay(now.AddDate(0, 0, 1))
		if quota, found := q.findQuota(ak, "month"); found && quota.MaxRequests != nil {
			sum, err := q.st.SumDayRequests(ak.ID, monthStart, tomorrowUTC)
			if err == nil && sum >= *quota.MaxRequests {
				writeQuotaError(w, "month", *quota.MaxRequests)
				return
			}
		}

		// 3. Day (atomic reserve + compare-and-rollback).
		dayUTC := truncateToDay(now)
		if quota, found := q.findQuota(ak, "day"); found && quota.MaxRequests != nil {
			counter, err := q.st.ReserveQuota(ak.ID, dayUTC, 1)
			if err == nil && counter.RequestCount > *quota.MaxRequests {
				// Rollback the reservation we just made.
				_, _ = q.st.ReserveQuota(ak.ID, dayUTC, -1)
				writeQuotaError(w, "day", *quota.MaxRequests)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// findQuota returns the most-specific quota for the key + window.
// Key-scope takes precedence over team-scope.
func (q *QuotaMW) findQuota(ak store.APIKey, window string) (store.Quota, bool) {
	quota, err := q.st.GetQuota("key", ak.ID, window)
	if err == nil {
		return quota, true
	}
	if ak.TeamID != "" {
		quota, err = q.st.GetQuota("team", ak.TeamID, window)
		if err == nil {
			return quota, true
		}
	}
	return store.Quota{}, false
}

// checkRPM atomically checks and increments the RPM counter.
// When persistRPM is true, uses SQLite-backed rpm_buckets (survives restarts).
// When false, uses in-memory map (v0.1 default, zero overhead).
// Returns true if the limit is exceeded (request should be rejected).
func (q *QuotaMW) checkRPM(keyID string, limit int64, now time.Time) bool {
	if q.persistRPM {
		minute := now.UTC().Unix() / 60
		count, err := q.st.GetAndIncrementRPM(keyID, minute)
		if err != nil {
			// Fallback to in-memory on DB error so a transient SQLite issue
			// doesn't disable rate limiting entirely.
			return q.checkRPMInMemory(keyID, limit, now)
		}
		return count > limit
	}
	return q.checkRPMInMemory(keyID, limit, now)
}

// checkRPMInMemory is the original in-memory RPM check.
func (q *QuotaMW) checkRPMInMemory(keyID string, limit int64, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	slot, ok := q.rpm[keyID]
	if !ok || now.After(slot.windowEnd) {
		q.rpm[keyID] = &rpmSlot{count: 1, windowEnd: now.Truncate(time.Minute).Add(time.Minute)}
		return false
	}
	slot.count++
	return slot.count > limit
}

// truncateToDay returns the ms-epoch of 00:00:00 UTC on the day of t.
func truncateToDay(t time.Time) int64 {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// truncateToMonth returns the ms-epoch of 00:00:00 UTC on the first of the month of t.
func truncateToMonth(t time.Time) int64 {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
}

func writeQuotaError(w http.ResponseWriter, window string, limit int64) {
	writeStructuredError(w, http.StatusTooManyRequests, "quota_exceeded",
		fmt.Sprintf("%s request limit of %d exceeded", window, limit),
		"check your quota configuration or contact your gateway admin")
}

// Ensure the errors import is used (ErrNotFound path in findQuota uses errors.Is).
var _ = errors.New
