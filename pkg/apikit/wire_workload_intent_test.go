package apikit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWorkloadIntentWireRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)

	srv := ServerIntent{
		ServerID:          "srv-1",
		Namespace:         "mcp-team-alpha",
		Name:              "billing",
		TeamID:            "team-1",
		DesiredRevisionID: "rev-1",
		Lifecycle:         ServerLifecycleRetiring,
		SpecJSON:          json.RawMessage(`{"image":"registry.local/alpha/billing:v1"}`),
		SpecDigest:        "sha256:abc",
		PublicationID:     "pub-1",
		RetirementCheckpoints: ServerRetirementCheckpoints{
			RetiringMarked:     true,
			SessionsFenced:     true,
			GrantsRetired:      false,
			CredentialsCleaned: false,
			CRDeleted:          false,
		},
		TargetCRUID: "uid-cr-1",
		UpdatedAt:   now,
		Receipt: &WorkloadExecutionReceipt{
			FenceEpoch:        2,
			FenceHolder:       "worker-1",
			AppliedRevisionID: "rev-1",
			Outcome:           WorkloadIntentOutcomeApplied,
			TargetUID:         "uid-cr-1",
			TargetGeneration:  3,
			SpecDigest:        "sha256:abc",
			Attempt:           1,
			WorkerID:          "worker-1",
			UpdatedAt:         now,
		},
		Observation: &WorkloadStatusObservation{
			ObserverID:         "obs-1",
			ObserverEpoch:      4,
			ObservedRevisionID: "rev-1",
			TargetUID:          "uid-cr-1",
			ObservedGeneration: 3,
			Health:             WorkloadIntentHealthReady,
			LastTransitionAt:   now,
			LastVerifiedAt:     now,
			UpdatedAt:          now,
		},
	}

	raw, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal ServerIntent: %v", err)
	}
	var got ServerIntent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal ServerIntent: %v", err)
	}
	if got.ServerID != srv.ServerID || got.Lifecycle != ServerLifecycleRetiring || !got.RetirementCheckpoints.SessionsFenced || got.RetirementCheckpoints.GrantsRetired {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Receipt == nil || got.Receipt.TargetGeneration != 3 || got.Observation == nil || got.Observation.Health != WorkloadIntentHealthReady {
		t.Fatalf("receipt/observation mismatch: %+v", got)
	}

	restart := RestartOperationIntent{
		OperationID:       "op-1",
		RequestedBy:       "admin-1",
		TargetComponents:  []string{"gateway", "ui"},
		RestartedAt:       "2026-11-25T13:00:00Z",
		DesiredRevisionID: "rev-restart-1",
		Status:            RestartStatusInProgress,
		ComponentCheckpoints: map[string]ComponentRestartCheckpoint{
			"gateway": {Component: "gateway", Completed: true, TargetUID: "dep-uid-1"},
		},
		UpdatedAt: now,
	}
	rawRestart, err := json.Marshal(restart)
	if err != nil {
		t.Fatalf("marshal RestartOperationIntent: %v", err)
	}
	var gotRestart RestartOperationIntent
	if err := json.Unmarshal(rawRestart, &gotRestart); err != nil {
		t.Fatalf("unmarshal RestartOperationIntent: %v", err)
	}
	if gotRestart.RestartedAt != "2026-11-25T13:00:00Z" || !gotRestart.ComponentCheckpoints["gateway"].Completed {
		t.Fatalf("restart round-trip mismatch: %+v", gotRestart)
	}
}
