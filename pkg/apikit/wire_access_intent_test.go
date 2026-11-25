package apikit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestComputeGrantGatewayPropagationSeparatesMilestones(t *testing.T) {
	now := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	revokedAt := now.Add(1 * time.Second)
	appliedAt := now.Add(2 * time.Second)
	verifiedAt := now.Add(3 * time.Second)

	t.Run("revocation committed only is neither applied nor propagated", func(t *testing.T) {
		p := ComputeGrantGatewayPropagation(GrantIntentStateRevoked, "rev-2", &revokedAt, nil, nil)
		if !p.RevocationCommitted {
			t.Fatal("expected RevocationCommitted=true")
		}
		if p.ProjectionApplied {
			t.Fatal("expected ProjectionApplied=false without receipt")
		}
		if p.PropagationVerified {
			t.Fatal("expected PropagationVerified=false without observation")
		}
		if p.PropagationStatus != GrantPropagationUnknown {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationUnknown)
		}
	})

	t.Run("projection applied without gateway observation stays unverified", func(t *testing.T) {
		receipt := &GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			Outcome:           AccessIntentOutcomeCleaned,
			AppliedAt:         &appliedAt,
		}
		p := ComputeGrantGatewayPropagation(GrantIntentStateRevoked, "rev-2", &revokedAt, receipt, nil)
		if !p.RevocationCommitted || !p.ProjectionApplied {
			t.Fatalf("expected committed and applied: %+v", p)
		}
		if p.PropagationVerified {
			t.Fatal("ProjectionApplied must never imply PropagationVerified")
		}
		if p.PropagationStatus != GrantPropagationUnknown {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationUnknown)
		}
	})

	t.Run("partial replica acknowledgement stays unverified", func(t *testing.T) {
		receipt := &GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			Outcome:           AccessIntentOutcomeCleaned,
			AppliedAt:         &appliedAt,
		}
		obs := &GrantObservation{
			ObserverEpoch:        1,
			ObservedRevisionID:   "rev-2",
			PropagationStatus:    GrantPropagationPending,
			ServingReplicas:      3,
			AcknowledgedReplicas: 2,
			LastVerifiedAt:       verifiedAt,
		}
		p := ComputeGrantGatewayPropagation(GrantIntentStateRevoked, "rev-2", &revokedAt, receipt, obs)
		if p.PropagationVerified {
			t.Fatal("partial replica ack must not be PropagationVerified")
		}
		if p.PropagationStatus != GrantPropagationPending {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationPending)
		}
	})

	t.Run("claimed verified with zero replicas or mismatch is downgraded to unverified", func(t *testing.T) {
		receipt := &GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			Outcome:           AccessIntentOutcomeCleaned,
			AppliedAt:         &appliedAt,
		}
		obs := &GrantObservation{
			ObserverEpoch:        1,
			ObservedRevisionID:   "rev-2",
			PropagationStatus:    GrantPropagationVerified,
			ServingReplicas:      2,
			AcknowledgedReplicas: 1,
			LastVerifiedAt:       verifiedAt,
		}
		p := ComputeGrantGatewayPropagation(GrantIntentStateRevoked, "rev-2", &revokedAt, receipt, obs)
		if p.PropagationVerified {
			t.Fatal("mismatched replica counts must not be PropagationVerified")
		}
		if p.PropagationStatus != GrantPropagationUnverified {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationUnverified)
		}
	})

	t.Run("all serving replicas acknowledged matching revision is verified", func(t *testing.T) {
		receipt := &GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			Outcome:           AccessIntentOutcomeCleaned,
			AppliedAt:         &appliedAt,
		}
		obs := &GrantObservation{
			ObserverEpoch:        1,
			ObservedRevisionID:   "rev-2",
			PropagationStatus:    GrantPropagationVerified,
			ServingReplicas:      3,
			AcknowledgedReplicas: 3,
			LastVerifiedAt:       verifiedAt,
		}
		p := ComputeGrantGatewayPropagation(GrantIntentStateRevoked, "rev-2", &revokedAt, receipt, obs)
		if !p.RevocationCommitted || !p.ProjectionApplied || !p.PropagationVerified {
			t.Fatalf("expected all milestones verified: %+v", p)
		}
		if p.PropagationStatus != GrantPropagationVerified {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationVerified)
		}
		if p.PropagationVerifiedAt == nil || !p.PropagationVerifiedAt.Equal(verifiedAt) {
			t.Fatalf("PropagationVerifiedAt = %v, want %v", p.PropagationVerifiedAt, verifiedAt)
		}
	})

	t.Run("stale observation for older revision stays unverified", func(t *testing.T) {
		receipt := &GrantReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-2",
			Outcome:           AccessIntentOutcomeApplied,
			AppliedAt:         &appliedAt,
		}
		obs := &GrantObservation{
			ObserverEpoch:        1,
			ObservedRevisionID:   "rev-1",
			PropagationStatus:    GrantPropagationVerified,
			ServingReplicas:      2,
			AcknowledgedReplicas: 2,
			LastVerifiedAt:       verifiedAt,
		}
		p := ComputeGrantGatewayPropagation(GrantIntentStateActive, "rev-2", nil, receipt, obs)
		if p.PropagationVerified {
			t.Fatal("observation for older revision must not verify newer revision")
		}
		if p.PropagationStatus != GrantPropagationUnverified {
			t.Fatalf("PropagationStatus = %q, want %q", p.PropagationStatus, GrantPropagationUnverified)
		}
	})
}

func TestAccessIntentWireJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	grant := GrantIntent{
		GrantID:           "grant-1",
		Namespace:         "mcp-team-alpha",
		Name:              "allow-read",
		TeamID:            "team-1",
		ServerName:        "docs",
		ServerUID:         "srv-uid-1",
		Subject:           GrantSubjectSummary{HumanID: "user-1", AgentID: "agt-1", TeamID: "team-1"},
		SpecJSON:          json.RawMessage(`{"mode":"enforce"}`),
		DesiredRevisionID: "rev-1",
		State:             GrantIntentStateActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		t.Fatalf("marshal GrantIntent: %v", err)
	}
	var decoded GrantIntent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal GrantIntent: %v", err)
	}
	if decoded.GrantID != grant.GrantID || decoded.ServerUID != grant.ServerUID || decoded.DesiredRevisionID != grant.DesiredRevisionID {
		t.Fatalf("decoded grant mismatch: %+v", decoded)
	}

	enrollment := EnrollmentIntent{
		OperationID:            "op-1",
		SessionUID:             "sess-uid-1",
		ServerUID:              "srv-uid-1",
		AgentID:                "agt-1",
		Namespace:              "mcp-team-alpha",
		SessionName:            "sess-1",
		CSRSHA256:              "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DesiredRevisionID:      "rev-e1",
		State:                  EnrollmentStatePending,
		CertificateRequestName: "enroll-abcdef",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	raw, err = json.Marshal(enrollment)
	if err != nil {
		t.Fatalf("marshal EnrollmentIntent: %v", err)
	}
	var decodedEnroll EnrollmentIntent
	if err := json.Unmarshal(raw, &decodedEnroll); err != nil {
		t.Fatalf("unmarshal EnrollmentIntent: %v", err)
	}
	if decodedEnroll.OperationID != enrollment.OperationID || decodedEnroll.CSRSHA256 != enrollment.CSRSHA256 {
		t.Fatalf("decoded enrollment mismatch: %+v", decodedEnroll)
	}

	cleanup := AgentCleanupIntent{
		AgentID:              "agt-1",
		TeamID:               "team-1",
		DesiredRevisionID:    "rev-c1",
		DeactivatedAt:        now,
		RevokedSessionsCount: 4,
		UpdatedAt:            now,
	}
	raw, err = json.Marshal(cleanup)
	if err != nil {
		t.Fatalf("marshal AgentCleanupIntent: %v", err)
	}
	var decodedCleanup AgentCleanupIntent
	if err := json.Unmarshal(raw, &decodedCleanup); err != nil {
		t.Fatalf("unmarshal AgentCleanupIntent: %v", err)
	}
	if decodedCleanup.AgentID != cleanup.AgentID || decodedCleanup.RevokedSessionsCount != 4 {
		t.Fatalf("decoded cleanup mismatch: %+v", decodedCleanup)
	}
}
