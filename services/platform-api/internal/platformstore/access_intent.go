package platformstore

import (
	"context"

	"mcp-platform-api/internal/accessintent"
	"mcp-runtime/pkg/apikit"
)

// ListGrantIntents returns all durable grant intents when the opt-in access
// intent journal is enabled.
func (s *Store) ListGrantIntents(ctx context.Context) ([]apikit.GrantIntent, error) {
	if s == nil || s.accessIntents == nil {
		return nil, accessintent.ErrDisabled
	}
	return s.accessIntents.ListGrantIntents(ctx)
}

// UpsertGrantIntent atomically persists desired grant intent and schedules
// reconciliation.
func (s *Store) UpsertGrantIntent(ctx context.Context, req apikit.GrantIntentUpsertRequest) (apikit.GrantIntent, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantIntent{}, accessintent.ErrDisabled
	}
	return s.accessIntents.UpsertGrantIntent(ctx, req)
}

// GetGrantIntent returns one durable grant intent by grant incarnation ID.
func (s *Store) GetGrantIntent(ctx context.Context, grantID string) (apikit.GrantIntent, bool, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantIntent{}, false, accessintent.ErrDisabled
	}
	return s.accessIntents.GetGrantIntent(ctx, grantID)
}

// RevokeGrantIntent commits authoritative grant revocation/deletion in Postgres
// FIRST and enqueues Kubernetes projection cleanup in the same transaction.
func (s *Store) RevokeGrantIntent(ctx context.Context, grantID string, deleteGrant bool) (apikit.GrantIntent, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantIntent{}, accessintent.ErrDisabled
	}
	return s.accessIntents.RevokeGrantIntent(ctx, grantID, deleteGrant)
}

// ClaimGrantIntentFence increments the grant's worker execution fence and
// returns its authoritative desired state.
func (s *Store) ClaimGrantIntentFence(ctx context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantIntentFenceResponse{}, accessintent.ErrDisabled
	}
	return s.accessIntents.ClaimGrantFence(ctx, grantID, workerID)
}

// RecordGrantIntentReceipt persists a fenced grant execution receipt.
func (s *Store) RecordGrantIntentReceipt(ctx context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantReceipt{}, accessintent.ErrDisabled
	}
	return s.accessIntents.RecordGrantReceipt(ctx, grantID, req)
}

// RecordGrantIntentObservation persists an independently observed CR and
// gateway replica propagation update.
func (s *Store) RecordGrantIntentObservation(ctx context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.GrantObservation{}, accessintent.ErrDisabled
	}
	return s.accessIntents.RecordGrantObservation(ctx, grantID, req)
}

// ListEnrollmentIntents returns all durable adapter enrollment operations.
func (s *Store) ListEnrollmentIntents(ctx context.Context) ([]apikit.EnrollmentIntent, error) {
	if s == nil || s.accessIntents == nil {
		return nil, accessintent.ErrDisabled
	}
	return s.accessIntents.ListEnrollmentIntents(ctx)
}

// CreateEnrollmentIntent idempotently records a durable adapter enrollment
// operation keyed to (session_uid, server_uid, csr_sha256).
func (s *Store) CreateEnrollmentIntent(ctx context.Context, req apikit.EnrollmentIntentCreateRequest) (apikit.EnrollmentIntent, bool, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.EnrollmentIntent{}, false, accessintent.ErrDisabled
	}
	return s.accessIntents.CreateEnrollmentIntent(ctx, req)
}

// GetEnrollmentIntent returns one durable adapter enrollment operation.
func (s *Store) GetEnrollmentIntent(ctx context.Context, opID string) (apikit.EnrollmentIntent, bool, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.EnrollmentIntent{}, false, accessintent.ErrDisabled
	}
	return s.accessIntents.GetEnrollmentIntent(ctx, opID)
}

// ClaimEnrollmentIntentFence increments the enrollment operation's execution
// fence and returns authoritative operation state.
func (s *Store) ClaimEnrollmentIntentFence(ctx context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.EnrollmentIntentFenceResponse{}, accessintent.ErrDisabled
	}
	return s.accessIntents.ClaimEnrollmentFence(ctx, opID, workerID)
}

// RecordEnrollmentIntentReceipt persists a fenced enrollment execution receipt.
func (s *Store) RecordEnrollmentIntentReceipt(ctx context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.EnrollmentReceipt{}, accessintent.ErrDisabled
	}
	return s.accessIntents.RecordEnrollmentReceipt(ctx, opID, req)
}

// ActivateEnrollmentIntent atomically re-verifies session/agent/server
// authority before activating an issued enrollment.
func (s *Store) ActivateEnrollmentIntent(ctx context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.EnrollmentIntent{}, accessintent.ErrDisabled
	}
	return s.accessIntents.ActivateEnrollment(ctx, opID, req)
}

// GetAgentCleanupIntent returns durable post-deactivation cleanup state for an agent.
func (s *Store) GetAgentCleanupIntent(ctx context.Context, agentID string) (apikit.AgentCleanupIntent, bool, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.AgentCleanupIntent{}, false, accessintent.ErrDisabled
	}
	return s.accessIntents.GetAgentCleanupIntent(ctx, agentID)
}

// ClaimAgentCleanupIntentFence increments the agent cleanup execution fence.
func (s *Store) ClaimAgentCleanupIntentFence(ctx context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.AgentCleanupFenceResponse{}, accessintent.ErrDisabled
	}
	return s.accessIntents.ClaimAgentCleanupFence(ctx, agentID, workerID)
}

// RecordAgentCleanupIntentReceipt persists a fenced post-deactivation cleanup receipt.
func (s *Store) RecordAgentCleanupIntentReceipt(ctx context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.AgentCleanupReceipt{}, accessintent.ErrDisabled
	}
	return s.accessIntents.RecordAgentCleanupReceipt(ctx, agentID, req)
}

// ClaimAccessIntentObserverLease increments the monotonic observer epoch for
// access intent status observation.
func (s *Store) ClaimAccessIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error) {
	if s == nil || s.accessIntents == nil {
		return apikit.AccessIntentObserverLeaseResponse{}, accessintent.ErrDisabled
	}
	return s.accessIntents.ClaimObserverLease(ctx, clusterKey, observerID)
}
