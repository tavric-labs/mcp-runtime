package platformstore

import (
	"context"

	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
)

// ListTeamIntents returns all recorded durable team namespace intents.
func (s *Store) ListTeamIntents(ctx context.Context) ([]apikit.TeamNamespaceIntent, error) {
	if s == nil || s.teamIntents == nil {
		return nil, teamintent.ErrDisabled
	}
	return s.teamIntents.ListIntents(ctx)
}

// GetTeamIntent returns the authoritative durable intent for one team incarnation.
func (s *Store) GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	if s == nil || s.teamIntents == nil {
		return apikit.TeamNamespaceIntent{}, false, teamintent.ErrDisabled
	}
	return s.teamIntents.GetIntent(ctx, teamID)
}

// ClaimTeamIntentFence increments the team's worker execution fence and returns
// its authoritative desired state.
func (s *Store) ClaimTeamIntentFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error) {
	if s == nil || s.teamIntents == nil {
		return apikit.TeamIntentFenceResponse{}, teamintent.ErrDisabled
	}
	return s.teamIntents.ClaimFence(ctx, teamID, workerID)
}

// RecordTeamIntentReceipt persists a fenced worker execution receipt.
func (s *Store) RecordTeamIntentReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error) {
	if s == nil || s.teamIntents == nil {
		return apikit.TeamNamespaceReceipt{}, teamintent.ErrDisabled
	}
	return s.teamIntents.RecordReceipt(ctx, teamID, req)
}

// ClaimTeamIntentObserverLease increments the monotonic status observer epoch.
func (s *Store) ClaimTeamIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error) {
	if s == nil || s.teamIntents == nil {
		return apikit.TeamIntentObserverLeaseResponse{}, teamintent.ErrDisabled
	}
	return s.teamIntents.ClaimObserverLease(ctx, clusterKey, observerID)
}

// RecordTeamIntentObservation persists a fenced independent status observation.
func (s *Store) RecordTeamIntentObservation(ctx context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error) {
	if s == nil || s.teamIntents == nil {
		return apikit.TeamNamespaceObservation{}, teamintent.ErrDisabled
	}
	return s.teamIntents.RecordObservation(ctx, teamID, req)
}
