package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func newTestSessionSync(t *testing.T, handler http.HandlerFunc) *gatewaySessionSync {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-projected-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return &gatewaySessionSync{server: sessionstate.Server{Namespace: "team", Name: "server", UID: "server-uid"}, baseURL: server.URL, token: newTokenFileSource(path), client: server.Client(), staleWindow: time.Minute}
}

func testSyncSession(name string) sessionstate.Session {
	return sessionstate.Session{ID: "team/" + name, Namespace: "team", ServerNamespace: "team", ServerName: "server", ServerUID: "server-uid", Binding: policy.Binding{Name: policy.SessionID(name), Namespace: "team", HumanID: "human", AgentID: "agent", TeamID: "team-id", ConsentedTrust: "medium", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}
}

func TestSessionSyncLargeSnapshotRevocationAndRestore(t *testing.T) {
	var mu sync.Mutex
	epoch := "first"
	revoked := false
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-projected-token" {
			t.Error("projected credential missing")
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == sessionstate.GatewayPath {
			snapshot := sessionstate.Snapshot{Epoch: epoch, Sequence: 1}
			for i := 0; i < 10000; i++ {
				if !revoked || i != 0 {
					snapshot.Sessions = append(snapshot.Sessions, testSyncSession(fmt.Sprintf("session-%d", i)))
				}
			}
			_ = json.NewEncoder(w).Encode(snapshot)
		} else if r.URL.Path == sessionstate.GatewayPath+"/changes" {
			if r.URL.Query().Get("epoch") != epoch {
				w.WriteHeader(410)
				return
			}
			out := sessionstate.Changes{Epoch: epoch, Sequence: 2}
			if revoked {
				out.Changes = []sessionstate.Change{{Kind: "session", Sequence: 2, Operation: "delete", ID: "team/session-0"}}
			}
			_ = json.NewEncoder(w).Encode(out)
		} else {
			w.WriteHeader(404)
		}
	})
	if _, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session-0"}, time.Now()); reason != errSessionUnavailable {
		t.Fatalf("cold reason=%s", reason)
	}
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binding, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session-0"}, time.Now()); reason != nil || binding == nil {
		t.Fatalf("warm binding=%+v reason=%s", binding, reason)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session-0"}, time.Now()); reason != errSessionNotFound {
		t.Fatalf("revoked reason=%s", reason)
	}
	mu.Lock()
	epoch = "restored"
	mu.Unlock()
	if err := cache.sync(context.Background()); err != sessionstate.ErrReset {
		t.Fatalf("restore error=%v", err)
	}
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.lastSync = time.Now().Add(-2 * time.Minute)
	cache.mu.Unlock()
	if _, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session-1"}, time.Now()); reason != errSessionUnavailable {
		t.Fatalf("stale reason=%s", reason)
	}
}

func TestSessionSyncRejectsForeignGenerationAndKeepsLastGood(t *testing.T) {
	foreign := false
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		row := testSyncSession("session")
		if foreign {
			row.ServerUID = "old-generation"
		}
		_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{row}})
	})
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.epoch = ""
	cache.mu.Unlock()
	foreign = true
	if err := cache.sync(context.Background()); err == nil {
		t.Fatal("foreign generation accepted")
	}
	if binding, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, time.Now()); binding == nil || reason != nil {
		t.Fatalf("last good binding lost: %+v %s", binding, reason)
	}
}

func TestSessionSyncConcurrentReadsUseIndependentBindings(t *testing.T) {
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sessionstate.GatewayPath {
			_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{testSyncSession("session")}})
		} else {
			_ = json.NewEncoder(w).Encode(sessionstate.Changes{Epoch: "epoch", Sequence: 1})
		}
	})
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, time.Now())
				if b == nil || reason != nil {
					t.Error(reason)
					return
				}
				b.ConsentedTrust = "high"
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if err := cache.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	b, _ := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, time.Now())
	if b.ConsentedTrust != "medium" {
		t.Fatal("reader mutated cache")
	}
}

func TestSessionSyncKnownResetOrIdentityRejectionInvalidatesWarmState(t *testing.T) {
	for _, status := range []int{401, 403, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			responseStatus := 200
			cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
				if responseStatus != 200 {
					w.WriteHeader(responseStatus)
					return
				}
				_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{testSyncSession("session")}})
			})
			if err := cache.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			responseStatus = status
			if err := cache.sync(context.Background()); err == nil {
				t.Fatal("rejection accepted")
			}
			responseStatus = 503
			if err := cache.sync(context.Background()); err == nil {
				t.Fatal("outage accepted")
			}
			if binding, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, time.Now()); binding != nil || reason != errSessionUnavailable {
				t.Fatalf("invalid authority remained usable: %+v %s", binding, reason)
			}
		})
	}
}

func TestSessionSyncStopsServingAfterStaleWindow(t *testing.T) {
	t.Parallel()
	lastSync := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	cache := &gatewaySessionSync{
		lastSync:    lastSync,
		staleWindow: time.Minute,
		sessions: map[string]sessionstate.Session{
			"team/session": {Binding: policy.Binding{Name: "session", Namespace: "team"}},
		},
	}
	for _, age := range []time.Duration{time.Minute - time.Nanosecond, time.Minute, time.Minute + time.Nanosecond} {
		t.Run(age.String(), func(t *testing.T) {
			t.Parallel()
			now := lastSync.Add(age)
			wantFresh := age <= time.Minute
			if cache.Fresh(now) != wantFresh {
				t.Fatal("incorrect cache freshness at stale deadline")
			}
			binding, reason := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, now)
			if wantFresh {
				if binding == nil || reason != nil {
					t.Fatalf("fresh session unavailable: binding=%+v reason=%s", binding, reason)
				}
			} else if binding != nil || reason != errSessionUnavailable {
				t.Fatalf("stale authority remained usable: binding=%+v reason=%s", binding, reason)
			}
		})
	}
}

func TestSessionSyncInvalidationFencesInFlightRefresh(t *testing.T) {
	for _, mode := range []string{"snapshot", "changes"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusGone} {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				started, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/rejected" {
						w.WriteHeader(status)
						return
					}
					close(started)
					<-release
					row := testSyncSession("old-session")
					if mode == "snapshot" {
						_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "old-epoch", Sequence: 2, Sessions: []sessionstate.Session{row}})
					} else {
						_ = json.NewEncoder(w).Encode(sessionstate.Changes{Epoch: "old-epoch", Sequence: 2, Changes: []sessionstate.Change{{Kind: "session", Sequence: 2, Operation: "upsert", ID: row.ID, Session: &row}}})
					}
				})
				if mode == "changes" {
					cache.epoch, cache.sequence = "old-epoch", 1
					cache.lastSync = time.Now()
					cache.sessions = map[string]sessionstate.Session{}
				}
				done := make(chan error, 1)
				go func() { done <- cache.sync(context.Background()) }()
				<-started
				var rejected sessionstate.Session
				if err := cache.getJSON(context.Background(), "/rejected", &rejected); err == nil {
					t.Fatal("identity/reset rejection succeeded")
				}
				unblock()
				if err := <-done; err == nil {
					t.Fatal("in-flight response reinstalled invalidated authority")
				}
				if cache.Fresh(time.Now()) {
					t.Fatal("invalidated cache became fresh")
				}
				cache.mu.RLock()
				defer cache.mu.RUnlock()
				if len(cache.sessions) != 0 || cache.epoch != "" {
					t.Fatal("invalidated sessions were republished")
				}
			})
		}
	}
}

func TestSessionLookupRejectsResponseFromBeforeInvalidation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sessionstate.GatewayPath {
			_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "new-epoch", Sequence: 2})
			return
		}
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(testSyncSession("missing"))
	})
	cache.epoch, cache.sequence = "old-epoch", 1
	cache.lastSync = time.Now()
	cache.sessions = map[string]sessionstate.Session{}
	done := make(chan error, 1)
	go func() {
		binding, err := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "missing"}, time.Now())
		if binding != nil {
			done <- fmt.Errorf("old lookup returned authority: %+v", binding)
			return
		}
		done <- err
	}()
	<-started
	cache.invalidate()
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cache.Fresh(time.Now()) {
		t.Fatal("replacement snapshot is not fresh")
	}
	unblock()
	if err := <-done; err != errSessionUnavailable {
		t.Fatalf("old lookup error=%v, want unavailable", err)
	}
}

func TestSessionSyncRejectsOutOfOrderRefresh(t *testing.T) {
	for _, mode := range []string{"snapshot", "changes"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var requests atomic.Int32
			cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
				first := requests.Add(1) == 1
				if first {
					close(started)
					<-release
				}
				row := testSyncSession("old-session")
				sequence := int64(3)
				if first {
					sequence = 2
				}
				if mode == "snapshot" {
					snapshot := sessionstate.Snapshot{Epoch: "epoch", Sequence: sequence}
					if first {
						snapshot.Sessions = []sessionstate.Session{row}
					}
					_ = json.NewEncoder(w).Encode(snapshot)
				} else {
					change := sessionstate.Change{Kind: "session", Sequence: sequence, Operation: "delete", ID: row.ID}
					if first {
						change.Operation, change.Session = "upsert", &row
					}
					_ = json.NewEncoder(w).Encode(sessionstate.Changes{Epoch: "epoch", Sequence: sequence, Changes: []sessionstate.Change{change}})
				}
			})
			if mode == "changes" {
				cache.epoch, cache.sequence = "epoch", 1
				cache.lastSync = time.Now()
				row := testSyncSession("old-session")
				cache.sessions = map[string]sessionstate.Session{row.ID: row}
			}
			done := make(chan error, 1)
			go func() { done <- cache.sync(context.Background()) }()
			<-started
			if err := cache.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			unblock()
			if err := <-done; err == nil {
				t.Fatal("older response replaced newer session state")
			}
			cache.mu.RLock()
			defer cache.mu.RUnlock()
			if cache.sequence != 3 || len(cache.sessions) != 0 {
				t.Fatalf("state regressed: sequence=%d sessions=%d", cache.sequence, len(cache.sessions))
			}
		})
	}
}
