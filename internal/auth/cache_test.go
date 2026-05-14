package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// cache_test.go covers T6: epoch-gated in-memory key cache. Per plan R3
// FINAL the cache stores lookup results (prefix → APIKey) for TTL=60s so
// the bcrypt verify + DB lookup only pays their cost once per minute per
// key. After TTL the next request re-fetches from the DB.
//
// Tests use a short TTL (10ms) so expiry can be observed without sleeping long.

func fakeKey(prefix, scope string) store.APIKey {
	return store.APIKey{
		ID:     "ak_test",
		TeamID: "tm_test",
		Prefix: prefix,
		Hash:   "bcrypt-hash-placeholder",
		Scope:  scope,
	}
}

func TestKeyCache_GetMiss_EmptyCache(t *testing.T) {
	c := auth.NewKeyCache(time.Minute)
	if _, ok := c.Get("lgw_prefix0000000xxx"); ok {
		t.Error("empty cache should always miss")
	}
}

func TestKeyCache_PutGet_ReturnsEntry(t *testing.T) {
	c := auth.NewKeyCache(time.Minute)
	k := fakeKey("lgw_prefix0000000aaa", "inbound")
	c.Put(k)

	got, ok := c.Get("lgw_prefix0000000aaa")
	if !ok {
		t.Fatal("cache miss after Put")
	}
	if got.ID != k.ID || got.Scope != k.Scope {
		t.Errorf("cache round-trip mismatch: %+v", got)
	}
}

func TestKeyCache_TTLExpiry_ReturnsMiss(t *testing.T) {
	c := auth.NewKeyCache(5 * time.Millisecond)
	k := fakeKey("lgw_prefix0000000bbb", "inbound")
	c.Put(k)

	time.Sleep(20 * time.Millisecond) // well past TTL

	if _, ok := c.Get("lgw_prefix0000000bbb"); ok {
		t.Error("expired entry should be a miss")
	}
}

func TestKeyCache_Evict_RemovesEntry(t *testing.T) {
	c := auth.NewKeyCache(time.Minute)
	k := fakeKey("lgw_prefix0000000ccc", "mcp_admin")
	c.Put(k)

	c.Evict("lgw_prefix0000000ccc")

	if _, ok := c.Get("lgw_prefix0000000ccc"); ok {
		t.Error("evicted entry should be a miss")
	}
}

func TestKeyCache_Evict_MissingKey_NoOp(t *testing.T) {
	c := auth.NewKeyCache(time.Minute)
	// Evicting a key that was never put must not panic.
	c.Evict("lgw_no_such_key00xxx")
}

func TestKeyCache_Concurrent_NoDataloss(t *testing.T) {
	// N writers putting distinct keys, then N readers verifying they land.
	c := auth.NewKeyCache(time.Minute)
	const N = 50
	keys := make([]store.APIKey, N)
	for i := range keys {
		p := [20]byte{}
		p[0], p[1], p[2], p[3] = 'l', 'g', 'w', '_'
		p[4] = byte('a' + i%26)
		p[5] = byte('a' + (i/26)%26)
		keys[i] = store.APIKey{ID: "ak_" + string(rune('a'+i%26)), Prefix: string(p[:]), Scope: "inbound"}
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := range keys {
		i := i
		go func() {
			defer wg.Done()
			c.Put(keys[i])
		}()
	}
	wg.Wait()

	for i, k := range keys {
		if _, ok := c.Get(k.Prefix); !ok {
			t.Errorf("key %d missing after concurrent puts", i)
		}
	}
}

func TestAPIKeyFromContext_RoundTrip(t *testing.T) {
	k := fakeKey("lgw_prefix0000000ddd", "inbound")
	ctx := auth.NewContext(context.Background(), k)

	got, ok := auth.APIKeyFromContext(ctx)
	if !ok {
		t.Fatal("APIKeyFromContext: should find key in context")
	}
	if got.Prefix != k.Prefix || got.Scope != k.Scope {
		t.Errorf("context round-trip mismatch: %+v", got)
	}
}

func TestAPIKeyFromContext_Missing(t *testing.T) {
	if _, ok := auth.APIKeyFromContext(context.Background()); ok {
		t.Error("empty context should not have a key")
	}
}
