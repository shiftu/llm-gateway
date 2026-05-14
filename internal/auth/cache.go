// Package auth provides the inbound API-key verification cache and context
// helpers used by the gateway's HTTP middleware (T6) and MCP access-control
// layer (T7). The cache stores verified lookup results (prefix → APIKey) for
// up to TTL so that bcrypt verification + SQLite reads don't pay their cost
// on every request. After TTL the next request re-fetches from the DB.
package auth

import (
	"context"
	"sync"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

// contextKey is an unexported type so our value can never collide with other
// packages' context keys, even if they also use a plain int.
type contextKey int

const apiKeyCtxKey contextKey = 0

// KeyCache is a TTL-gated in-memory store for verified API key lookups.
// It is safe for concurrent use. The zero value is NOT usable — use NewKeyCache.
type KeyCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	key       store.APIKey
	expiresAt time.Time
}

// NewKeyCache returns a KeyCache with the given TTL. The production default
// is 60s (plan R3 FINAL). Tests may pass shorter durations to observe expiry
// without long sleeps.
func NewKeyCache(ttl time.Duration) *KeyCache {
	return &KeyCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

// Get returns the cached APIKey for prefix and true if the entry exists and
// has not expired. Returns (zero, false) on miss or expiry. Expired entries
// are lazily deleted on access.
func (c *KeyCache) Get(prefix string) (store.APIKey, bool) {
	c.mu.RLock()
	e, ok := c.entries[prefix]
	c.mu.RUnlock()
	if !ok {
		return store.APIKey{}, false
	}
	if time.Now().After(e.expiresAt) {
		c.mu.Lock()
		delete(c.entries, prefix)
		c.mu.Unlock()
		return store.APIKey{}, false
	}
	return e.key, true
}

// Put inserts or refreshes the cache entry for ak.Prefix with a new TTL
// starting from now. Calling Put again before TTL expires resets the clock.
func (c *KeyCache) Put(ak store.APIKey) {
	c.mu.Lock()
	c.entries[ak.Prefix] = &cacheEntry{
		key:       ak,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// Evict removes the entry for prefix if it exists. Used by the MCP
// revoke_api_key tool (T8) to force immediate re-verification after revocation
// instead of waiting for TTL. Evicting a non-existent key is a no-op.
func (c *KeyCache) Evict(prefix string) {
	c.mu.Lock()
	delete(c.entries, prefix)
	c.mu.Unlock()
}

// NewContext returns a copy of ctx with the given APIKey stored under the
// package-private key. The quota middleware (T10) and MCP access-control
// layer (T7) retrieve it with APIKeyFromContext.
func NewContext(ctx context.Context, ak store.APIKey) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey, ak)
}

// APIKeyFromContext retrieves the APIKey stored by NewContext. Returns
// (zero, false) when no key is present — callers that require auth must
// treat this as a programming error (middleware should have populated it
// before the handler ran).
func APIKeyFromContext(ctx context.Context) (store.APIKey, bool) {
	ak, ok := ctx.Value(apiKeyCtxKey).(store.APIKey)
	return ak, ok
}
