package main

import (
	"context"
	"testing"
	"time"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

// Cache freshness is evaluated at an explicit instant: fresh through the end
// of the stale window, stale one tick later, and never fresh before a sync.
func TestSessionCacheFreshnessAroundItsDeadline(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	window := time.Minute
	binding := policy.Binding{Name: "session", Namespace: "team", HumanID: "human"}
	cache := &gatewaySessionSync{
		lastSync: synced, staleWindow: window,
		sessions: map[string]sessionstate.Session{"team/session": {Binding: binding}},
	}
	ref := SessionRef{Namespace: "team", ID: "session"}
	for _, tc := range []struct {
		name  string
		now   time.Time
		fresh bool
	}{
		{"before the sync (clock skew)", synced.Add(-time.Second), true},
		{"just inside the window", synced.Add(window - time.Nanosecond), true},
		{"at the deadline", synced.Add(window), true},
		{"just past the deadline", synced.Add(window + time.Nanosecond), false},
	} {
		if got := cache.Fresh(tc.now); got != tc.fresh {
			t.Errorf("%s: Fresh = %v, want %v", tc.name, got, tc.fresh)
		}
		_, err := cache.Lookup(context.Background(), ref, tc.now)
		if (err == nil) != tc.fresh {
			t.Errorf("%s: Lookup err = %v, fresh = %v", tc.name, err, tc.fresh)
		}
	}
	if (&gatewaySessionSync{staleWindow: window}).Fresh(synced) {
		t.Error("a never-synced cache reported fresh")
	}
	var missing *gatewaySessionSync
	if missing.Fresh(synced) {
		t.Error("a nil cache reported fresh")
	}
}

// A malformed expiry in a synced row makes the lookup unavailable rather than
// handing the binding to a caller that would have to interpret it.
func TestSessionLookupRejectsMalformedExpiryAtConstruction(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, expires := range []string{"tomorrow", "2026-10-25", "10:00"} {
		cache := &gatewaySessionSync{
			lastSync: now, staleWindow: time.Minute,
			sessions: map[string]sessionstate.Session{"team/session": {Binding: policy.Binding{Name: "session", Namespace: "team", ExpiresAt: expires}}},
		}
		if _, err := cache.Lookup(context.Background(), SessionRef{Namespace: "team", ID: "session"}, now); err != errSessionUnavailable {
			t.Errorf("expiry %q: err = %v", expires, err)
		}
	}
}
