package accessintentobserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
)

type fakeObserverPlatform struct {
	intents        []apikit.GrantIntent
	leaseEpoch     int64
	observations   []apikit.GrantIntentObservationRequest
	observationErr error
}

func (f *fakeObserverPlatform) ListGrantIntents(context.Context) ([]apikit.GrantIntent, error) {
	return f.intents, nil
}

func (f *fakeObserverPlatform) ClaimAccessIntentObserverLease(_ context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error) {
	f.leaseEpoch++
	return apikit.AccessIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: f.leaseEpoch,
	}, nil
}

func (f *fakeObserverPlatform) RecordGrantIntentObservation(_ context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error) {
	if f.observationErr != nil {
		return apikit.GrantObservation{}, f.observationErr
	}
	f.observations = append(f.observations, req)
	return apikit.GrantObservation{
		ObserverID:           req.ObserverID,
		ObserverEpoch:        req.ObserverEpoch,
		ObservedRevisionID:   req.ObservedRevisionID,
		TargetUID:            req.TargetUID,
		Health:               req.Health,
		PropagationStatus:    req.PropagationStatus,
		ServingReplicas:      req.ServingReplicas,
		AcknowledgedReplicas: req.AcknowledgedReplicas,
		GatewayEvidence:      req.GatewayEvidence,
		LastVerifiedAt:       req.VerifiedAt,
	}, nil
}

type fakeGrantReader struct {
	grants map[string]*ObservedGrantCR
	err    error
}

func (r *fakeGrantReader) GetGrantCR(_ context.Context, namespace, name string) (*ObservedGrantCR, bool, error) {
	if r.err != nil {
		return nil, false, r.err
	}
	cr, ok := r.grants[namespace+"/"+name]
	return cr, ok, nil
}

type fakeGatewayProber struct {
	replicas map[string][]apikit.GrantGatewayReplicaEvidence
	err      error
}

func (p *fakeGatewayProber) ProbeServingReplicas(_ context.Context, namespace, serverName, serverUID string) ([]apikit.GrantGatewayReplicaEvidence, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.replicas[serverUID], nil
}

func TestObserverSeparatesPartialAckUnreachableAndVerifiedPropagation(t *testing.T) {
	appliedAt := time.Now().UTC()
	grant := apikit.GrantIntent{
		GrantID:               "grant-1",
		Namespace:             "mcp-team-alpha",
		Name:                  "allow-docs",
		ServerName:            "docs",
		ServerUID:             "srv-uid-1",
		DesiredRevisionID:     "rev-2",
		State:                 apikit.GrantIntentStateRevoked,
		RevocationCommittedAt: &appliedAt,
		Receipt: &apikit.GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			TargetUID:         "cr-uid-1",
			SpecDigest:        "sha256:rev2",
			Outcome:           apikit.AccessIntentOutcomeCleaned,
			AppliedAt:         &appliedAt,
		},
	}

	t.Run("partial gateway replica ack stays Pending/Unverified until every replica matches", func(t *testing.T) {
		platform := &fakeObserverPlatform{intents: []apikit.GrantIntent{grant}}
		reader := &fakeGrantReader{grants: map[string]*ObservedGrantCR{}} // CR deleted (projection applied)
		prober := &fakeGatewayProber{
			replicas: map[string][]apikit.GrantGatewayReplicaEvidence{
				"srv-uid-1": {
					{PodName: "gw-0", Ready: true, Reachable: true, ServerUID: "srv-uid-1", PolicyRevision: "rev-2", PolicyDigest: "sha256:rev2"},
					{PodName: "gw-1", Ready: true, Reachable: true, ServerUID: "srv-uid-1", PolicyRevision: "rev-1", PolicyDigest: "sha256:rev1"},
				},
			},
		}
		obs := New(platform, reader, prober, Config{ObserverID: "obs-1", ClusterKey: "default"})
		if err := obs.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce err = %v", err)
		}
		if len(platform.observations) != 1 {
			t.Fatalf("observations = %d, want 1", len(platform.observations))
		}
		got := platform.observations[0]
		if got.PropagationStatus != apikit.GrantPropagationPending {
			t.Fatalf("PropagationStatus = %q, want %q", got.PropagationStatus, apikit.GrantPropagationPending)
		}
		if got.ServingReplicas != 2 || got.AcknowledgedReplicas != 1 {
			t.Fatalf("replicas = %d/%d, want 1/2", got.AcknowledgedReplicas, got.ServingReplicas)
		}

		// Now second replica converges to rev-2 -> transitions to Verified!
		prober.replicas["srv-uid-1"][1].PolicyRevision = "rev-2"
		prober.replicas["srv-uid-1"][1].PolicyDigest = "sha256:rev2"
		if err := obs.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce 2 err = %v", err)
		}
		got2 := platform.observations[1]
		if got2.PropagationStatus != apikit.GrantPropagationVerified {
			t.Fatalf("PropagationStatus = %q, want %q", got2.PropagationStatus, apikit.GrantPropagationVerified)
		}
		if got2.ServingReplicas != 2 || got2.AcknowledgedReplicas != 2 {
			t.Fatalf("replicas = %d/%d, want 2/2", got2.AcknowledgedReplicas, got2.ServingReplicas)
		}
	})

	t.Run("unreachable replica during churn is reported Unknown and never Verified", func(t *testing.T) {
		platform := &fakeObserverPlatform{intents: []apikit.GrantIntent{grant}}
		reader := &fakeGrantReader{grants: map[string]*ObservedGrantCR{}}
		prober := &fakeGatewayProber{
			replicas: map[string][]apikit.GrantGatewayReplicaEvidence{
				"srv-uid-1": {
					{PodName: "gw-0", Ready: true, Reachable: true, ServerUID: "srv-uid-1", PolicyRevision: "rev-2"},
					{PodName: "gw-1", Ready: false, Reachable: false, Error: "connection refused"},
				},
			},
		}
		obs := New(platform, reader, prober, Config{ObserverID: "obs-1", ClusterKey: "default"})
		if err := obs.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce err = %v", err)
		}
		got := platform.observations[0]
		if got.PropagationStatus != apikit.GrantPropagationUnknown {
			t.Fatalf("PropagationStatus = %q, want %q when a replica is unreachable", got.PropagationStatus, apikit.GrantPropagationUnknown)
		}
	})

	t.Run("zero serving replicas reports Unknown and never Verified", func(t *testing.T) {
		platform := &fakeObserverPlatform{intents: []apikit.GrantIntent{grant}}
		reader := &fakeGrantReader{grants: map[string]*ObservedGrantCR{}}
		prober := &fakeGatewayProber{replicas: map[string][]apikit.GrantGatewayReplicaEvidence{}}
		obs := New(platform, reader, prober, Config{ObserverID: "obs-1", ClusterKey: "default"})
		if err := obs.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce err = %v", err)
		}
		got := platform.observations[0]
		if got.PropagationStatus != apikit.GrantPropagationUnknown {
			t.Fatalf("PropagationStatus = %q, want %q when zero serving replicas", got.PropagationStatus, apikit.GrantPropagationUnknown)
		}
	})

	t.Run("stale observer epoch 409 resets lease epoch and returns ErrStaleLease", func(t *testing.T) {
		platform := &fakeObserverPlatform{
			intents:        []apikit.GrantIntent{grant},
			observationErr: platformapi.ErrAccessIntentConflict,
		}
		reader := &fakeGrantReader{grants: map[string]*ObservedGrantCR{}}
		prober := &fakeGatewayProber{
			replicas: map[string][]apikit.GrantGatewayReplicaEvidence{
				"srv-uid-1": {{PodName: "gw-0", Ready: true, Reachable: true, ServerUID: "srv-uid-1", PolicyRevision: "rev-2"}},
			},
		}
		obs := New(platform, reader, prober, Config{ObserverID: "obs-stale", ClusterKey: "default"})
		err := obs.ReconcileOnce(t.Context())
		if !errors.Is(err, ErrStaleObserverEpoch) {
			t.Fatalf("err = %v, want ErrStaleObserverEpoch", err)
		}
		if obs.Epoch() != 0 {
			t.Fatalf("expected observer epoch reset to 0 after 409, got %d", obs.Epoch())
		}
	})
}
