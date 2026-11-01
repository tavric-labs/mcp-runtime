package passport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPUserKeyResolverCachesNegativeResults(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer internal-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer server.Close()

	resolver := &HTTPUserKeyResolver{BaseURL: server.URL, Token: "internal-token"}
	for range 2 {
		_, ok, err := resolver.ResolveAPIKey(context.Background(), "missing-key")
		if err != nil || ok {
			t.Fatalf("ResolveAPIKey() ok=%v err=%v", ok, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver calls = %d, want 1", got)
	}
}

func TestHTTPUserKeyResolverRevalidatesSuccessfulAuth(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"principal": Principal{
					Subject: "user-1",
					Role:    RoleUser,
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer server.Close()

	resolver := &HTTPUserKeyResolver{BaseURL: server.URL, Token: "internal-token"}
	principal, ok, err := resolver.ResolveAPIKey(context.Background(), "revoked-key")
	if err != nil || !ok || principal.Subject != "user-1" {
		t.Fatalf("first ResolveAPIKey() principal=%#v ok=%v err=%v", principal, ok, err)
	}

	_, ok, err = resolver.ResolveAPIKey(context.Background(), "revoked-key")
	if err != nil || ok {
		t.Fatalf("second ResolveAPIKey() ok=%v err=%v, want revoked key to revalidate as false", ok, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls = %d, want 2", got)
	}
}

func TestHTTPUserKeyResolverBoundsNegativeCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer server.Close()
	resolver := &HTTPUserKeyResolver{BaseURL: server.URL, CacheTTL: time.Hour}
	const limit = 1024
	for i := 0; i < limit+8; i++ {
		if _, ok, err := resolver.ResolveAPIKey(context.Background(), fmt.Sprintf("missing-%d", i)); err != nil || ok {
			t.Fatalf("lookup=%d ok=%v err=%v", i, ok, err)
		}
	}
	resolver.mu.Lock()
	size := len(resolver.cache)
	resolver.mu.Unlock()
	if size > limit {
		t.Fatalf("negative cache grew to %d entries, maximum=%d", size, limit)
	}
	before := calls.Load()
	if _, ok, err := resolver.ResolveAPIKey(context.Background(), "missing-0"); err != nil || ok {
		t.Fatalf("evicted lookup ok=%v err=%v", ok, err)
	}
	if calls.Load() != before+1 {
		t.Fatal("oldest key was not evicted at capacity")
	}
}

func TestHTTPUserKeyResolverReclaimsExpiredNegativeEntries(t *testing.T) {
	resolver := &HTTPUserKeyResolver{}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("expired-%d", i)
		resolver.store(key, Principal{}, false)
		entry := resolver.cache[key]
		entry.expiresAt = time.Now().Add(-time.Minute)
		resolver.cache[key] = entry
	}
	resolver.store("current", Principal{}, false)
	if len(resolver.cache) != 1 {
		t.Fatalf("expired entries retained: count=%d", len(resolver.cache))
	}
	if _, _, found := resolver.cached("current"); !found {
		t.Fatal("current negative entry was removed")
	}
}

func TestHTTPUserKeyResolverConcurrentNegativeCache(t *testing.T) {
	resolver := &HTTPUserKeyResolver{}
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("worker-%d-key-%d", worker, i)
				resolver.store(key, Principal{}, false)
				resolver.cached(key)
			}
		}(worker)
	}
	workers.Wait()
	if len(resolver.cache) > 1024 {
		t.Fatalf("concurrent cache grew to %d", len(resolver.cache))
	}
}

func TestHTTPUserKeyResolverEvictsLeastRecentlyUsedMiss(t *testing.T) {
	resolver := &HTTPUserKeyResolver{CacheTTL: time.Hour}
	for i := 0; i < 1024; i++ {
		resolver.store(fmt.Sprintf("key-%d", i), Principal{}, false)
	}
	if _, _, found := resolver.cached("key-0"); !found {
		t.Fatal("initial miss not cached")
	}
	resolver.store("new-key", Principal{}, false)
	if _, _, found := resolver.cached("key-0"); !found {
		t.Fatal("recently accessed miss was evicted")
	}
	if _, _, found := resolver.cached("key-1"); found {
		t.Fatal("least recently used miss was retained")
	}
	if resolver.cacheOrder.Len() != len(resolver.cache) {
		t.Fatal("eviction metadata is not bounded by cache entries")
	}
}

func TestHTTPUserKeyResolverUpdatingMissDoesNotGrowEvictionMetadata(t *testing.T) {
	resolver := &HTTPUserKeyResolver{}
	for i := 0; i < 2000; i++ {
		resolver.store("same-key", Principal{}, false)
	}
	if len(resolver.cache) != 1 || resolver.cacheOrder.Len() != 1 {
		t.Fatal("repeated misses leaked entries or eviction metadata")
	}
	entry := resolver.cache["same-key"]
	entry.expiresAt = time.Now().Add(-time.Minute)
	resolver.cache["same-key"] = entry
	if _, _, found := resolver.cached("same-key"); found {
		t.Fatal("expired result was reused")
	}
	if len(resolver.cache) != 0 || resolver.cacheOrder.Len() != 0 {
		t.Fatal("expired entry metadata was retained")
	}
}
