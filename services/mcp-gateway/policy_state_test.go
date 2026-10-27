package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

func TestPolicySnapshotStatesAreDistinct(t *testing.T) {
	t.Parallel()
	doc := &policypkg.Document{Revision: "rev-1"}
	loaded := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

	zero := policySnapshot{}
	if zero.Ready() || zero.Document() != nil || zero.Revision() != "" || !zero.LoadedAt().IsZero() || zero.ReloadErr() != nil {
		t.Fatalf("zero snapshot is not an inert not-ready state: %+v", zero)
	}
	if got := readySnapshot(nil, loaded); got.Ready() || got.Document() != nil {
		t.Fatal("a nil document was activated as ready")
	}

	seed := seedSnapshot(doc)
	if seed.Ready() || seed.Document() != doc || seed.Revision() != "" || !seed.LoadedAt().IsZero() {
		t.Fatalf("seed must serve its document without claiming readiness: %+v", seed)
	}

	ready := readySnapshot(doc, loaded)
	if !ready.Ready() || ready.Document() != doc || ready.Revision() != "rev-1" || !ready.LoadedAt().Equal(loaded) || ready.ReloadErr() != nil {
		t.Fatalf("ready snapshot: %+v", ready)
	}
}

// A failed reload is diagnostics only: it keeps the last-known-good policy,
// revision and load time, and a later success clears it. Before any policy has
// validated it never makes the snapshot ready.
func TestFailedReloadKeepsLastKnownGoodButNeverFabricatesReadiness(t *testing.T) {
	t.Parallel()
	doc := &policypkg.Document{Revision: "rev-1"}
	loaded := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	failure := errors.New("rejected")

	retained := readySnapshot(doc, loaded).afterFailedReload(failure)
	if !retained.Ready() || retained.Document() != doc || retained.Revision() != "rev-1" || !retained.LoadedAt().Equal(loaded) {
		t.Fatalf("last-known-good lost after failed reload: %+v", retained)
	}
	if retained.ReloadErr() != failure {
		t.Fatal("reload failure not recorded")
	}

	never := seedSnapshot(doc).afterFailedReload(failure)
	if never.Ready() || never.Revision() != "" || never.ReloadErr() != failure {
		t.Fatalf("never-ready state became ready or lost its error: %+v", never)
	}
	if got, err := (&gatewayServer{}).policyAfter(never); !errors.Is(err, errPolicyUnavailable) || got != doc {
		t.Fatalf("never-ready policy must fail closed on the seed: %v %v", got, err)
	}

	next := readySnapshot(&policypkg.Document{Revision: "rev-2"}, loaded.Add(time.Minute))
	if next.ReloadErr() != nil || next.Revision() != "rev-2" {
		t.Fatalf("successful reload did not replace state: %+v", next)
	}
}

func (s *gatewayServer) policyAfter(snapshot policySnapshot) (*policypkg.Document, error) {
	s.snapshotPolicy(snapshot)
	return s.currentPolicy()
}

func TestReloadStampsLoadTimeFromTheServerClock(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "policy.json")
	writeStampedPolicy(t, file, nil)
	s := newCacheTestServer(file)
	clock := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.reloadPolicy(); err != nil {
		t.Fatal(err)
	}
	if got := s.loadPolicySnapshot().LoadedAt(); !got.Equal(clock) {
		t.Fatalf("LoadedAt = %s, want %s", got, clock)
	}
	clock = clock.Add(time.Hour)
	if err := os.WriteFile(file, []byte(`{"schema_version":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.reloadPolicy(); err == nil {
		t.Fatal("accepted invalid reload")
	}
	if got := s.loadPolicySnapshot().LoadedAt(); !got.Equal(clock.Add(-time.Hour)) {
		t.Fatalf("failed reload moved LoadedAt to %s", got)
	}
}

func TestFirstReloadFailureStaysNotReadyAndFailsToolCallsClosed(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newCacheTestServer(file)
	s.snapshotPolicy(seedSnapshot(s.defaultPolicyDocument()))
	if err := s.reloadPolicy(); err == nil {
		t.Fatal("accepted invalid first load")
	}
	snap := s.loadPolicySnapshot()
	if snap.Ready() || snap.ReloadErr() == nil || snap.Document() == nil {
		t.Fatalf("snapshot after first failed load: ready=%v err=%v doc=%v", snap.Ready(), snap.ReloadErr(), snap.Document())
	}
	if _, err := s.currentPolicy(); !errors.Is(err, errPolicyUnavailable) {
		t.Fatalf("currentPolicy err = %v", err)
	}
	decision := authorizeRequest(authorizationInput{
		Policy: snap.Document(), PolicyErr: errPolicyUnavailable,
		Inspection: inspectedRPC("tools/call", "echo", RPCRequest), DefaultPolicyVersion: "d",
	}, time.Now())
	if decision.Allowed || decision.Reason != "policy_unavailable" {
		t.Fatalf("decision = %+v", decision)
	}
}
