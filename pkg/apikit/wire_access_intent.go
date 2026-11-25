package apikit

import (
	"encoding/json"
	"time"
)

// GrantIntentState values describe the authoritative desired lifecycle state
// of one grant incarnation in Platform API.
const (
	GrantIntentStateActive  = "Active"
	GrantIntentStateRevoked = "Revoked"
	GrantIntentStateDeleted = "Deleted"
)

// AccessIntentOutcome values describe bounded execution receipts written by
// runtime workers for a specific intent incarnation and desired revision.
const (
	AccessIntentOutcomeApplied    = "Applied"
	AccessIntentOutcomeCleaned    = "Cleaned"
	AccessIntentOutcomeActivated  = "Activated"
	AccessIntentOutcomeSuperseded = "Superseded"
	AccessIntentOutcomeFailed     = "Failed"
	AccessIntentOutcomeTerminal   = "Terminal"
)

// AccessIntentErrorClass values classify worker reconciliation failures.
const (
	AccessIntentErrorTransient         = "transient"
	AccessIntentErrorOwnershipConflict = "ownership_conflict"
	AccessIntentErrorInvalidConfig     = "invalid_config"
	AccessIntentErrorAuthorityRevoked  = "authority_revoked"
	AccessIntentErrorExhausted         = "exhausted"
)

// AccessIntentHealth values describe independently observed Kubernetes and
// gateway health for access-control intents.
const (
	AccessIntentHealthPending     = "Pending"
	AccessIntentHealthProgressing = "Progressing"
	AccessIntentHealthReady       = "Ready"
	AccessIntentHealthDegraded    = "Degraded"
	AccessIntentHealthRevoked     = "Revoked"
	AccessIntentHealthDeleted     = "Deleted"
	AccessIntentHealthUnknown     = "Unknown"
)

// GrantPropagationStatus values describe independently verified gateway
// propagation for a grant revision. Propagation is never inferred from
// Kubernetes projection alone.
const (
	GrantPropagationVerified   = "Verified"
	GrantPropagationPending    = "Pending"
	GrantPropagationUnverified = "Unverified"
	GrantPropagationUnknown    = "Unknown"
)

// EnrollmentState values describe the durable lifecycle state of an adapter
// certificate enrollment operation. Private keys and raw secrets are never
// persisted in enrollment intents or receipts.
const (
	EnrollmentStatePending    = "Pending"
	EnrollmentStateSubmitted  = "Submitted"
	EnrollmentStateIssued     = "Issued"
	EnrollmentStateActivated  = "Activated"
	EnrollmentStateRevoked    = "Revoked"
	EnrollmentStateFailed     = "Failed"
	EnrollmentStateSuperseded = "Superseded"
)

// GrantSubjectSummary identifies the subject bound by a durable grant intent.
type GrantSubjectSummary struct {
	HumanID string `json:"human_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	TeamID  string `json:"team_id,omitempty"`
}

// GrantGatewayReplicaEvidence records the sanitized status reported by one
// serving gateway replica for a target MCPServer UID.
type GrantGatewayReplicaEvidence struct {
	PodName               string    `json:"pod_name"`
	Ready                 bool      `json:"ready"`
	Reachable             bool      `json:"reachable"`
	ServerUID             string    `json:"server_uid,omitempty"`
	PolicyRevision        string    `json:"policy_revision,omitempty"`
	PolicyDigest          string    `json:"policy_digest,omitempty"`
	SessionSyncReady      bool      `json:"session_sync_ready"`
	SessionSyncAgeSeconds *float64  `json:"session_sync_age_seconds,omitempty"`
	LastReloadError       string    `json:"last_reload_error,omitempty"`
	Error                 string    `json:"error,omitempty"`
	ObservedAt            time.Time `json:"observed_at,omitempty"`
}

// GrantGatewayPropagation separates authoritative database revocation,
// Kubernetes projection application, and independent gateway propagation
// verification into distinct milestones.
type GrantGatewayPropagation struct {
	RevocationCommitted   bool       `json:"revocation_committed"`
	RevocationCommittedAt *time.Time `json:"revocation_committed_at,omitempty"`
	ProjectionApplied     bool       `json:"projection_applied"`
	ProjectionAppliedAt   *time.Time `json:"projection_applied_at,omitempty"`
	PropagationVerified   bool       `json:"propagation_verified"`
	PropagationVerifiedAt *time.Time `json:"propagation_verified_at,omitempty"`
	PropagationStatus     string     `json:"propagation_status"`
}

// ComputeGrantGatewayPropagation derives strict milestone states from
// authoritative desired state, the latest execution receipt, and the latest
// independent gateway observation.
func ComputeGrantGatewayPropagation(
	state string,
	desiredRevisionID string,
	revocationCommittedAt *time.Time,
	receipt *GrantReceipt,
	observation *GrantObservation,
) GrantGatewayPropagation {
	out := GrantGatewayPropagation{
		RevocationCommitted: state == GrantIntentStateRevoked || state == GrantIntentStateDeleted,
		PropagationStatus:   GrantPropagationUnknown,
	}
	if out.RevocationCommitted && revocationCommittedAt != nil {
		t := revocationCommittedAt.UTC()
		out.RevocationCommittedAt = &t
	}
	if receipt != nil && desiredRevisionID != "" && receipt.AppliedRevisionID == desiredRevisionID {
		switch state {
		case GrantIntentStateActive:
			out.ProjectionApplied = receipt.Outcome == AccessIntentOutcomeApplied
		case GrantIntentStateRevoked, GrantIntentStateDeleted:
			out.ProjectionApplied = receipt.Outcome == AccessIntentOutcomeCleaned || receipt.Outcome == AccessIntentOutcomeApplied
		}
		if out.ProjectionApplied && receipt.AppliedAt != nil {
			t := receipt.AppliedAt.UTC()
			out.ProjectionAppliedAt = &t
		}
	}
	if observation == nil {
		return out
	}
	if desiredRevisionID != "" && observation.ObservedRevisionID != desiredRevisionID {
		out.PropagationStatus = GrantPropagationUnverified
		return out
	}
	allAcked := observation.ServingReplicas > 0 && observation.AcknowledgedReplicas == observation.ServingReplicas
	switch observation.PropagationStatus {
	case GrantPropagationVerified:
		if out.ProjectionApplied && allAcked {
			out.PropagationVerified = true
			out.PropagationStatus = GrantPropagationVerified
			if !observation.LastVerifiedAt.IsZero() {
				t := observation.LastVerifiedAt.UTC()
				out.PropagationVerifiedAt = &t
			}
			return out
		}
		out.PropagationStatus = GrantPropagationUnverified
	case GrantPropagationPending, GrantPropagationUnverified, GrantPropagationUnknown:
		out.PropagationStatus = observation.PropagationStatus
	default:
		out.PropagationStatus = GrantPropagationUnknown
	}
	return out
}

// GrantReceipt records the latest fenced execution state written by a runtime
// worker for one grant incarnation.
type GrantReceipt struct {
	FenceEpoch        int64      `json:"fence_epoch"`
	FenceHolder       string     `json:"fence_holder,omitempty"`
	AppliedRevisionID string     `json:"applied_revision_id,omitempty"`
	TargetUID         string     `json:"target_uid,omitempty"`
	TargetGeneration  int64      `json:"target_generation,omitempty"`
	SpecDigest        string     `json:"spec_digest,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	Attempt           int        `json:"attempt,omitempty"`
	WorkerID          string     `json:"worker_id,omitempty"`
	LastErrorClass    string     `json:"last_error_class,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	AppliedAt         *time.Time `json:"applied_at,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// GrantObservation records the latest independently observed Kubernetes CR and
// gateway replica propagation state for one grant incarnation.
type GrantObservation struct {
	ObserverID           string                        `json:"observer_id,omitempty"`
	ObserverEpoch        int64                         `json:"observer_epoch"`
	ObservedRevisionID   string                        `json:"observed_revision_id,omitempty"`
	TargetUID            string                        `json:"target_uid,omitempty"`
	Health               string                        `json:"health"`
	Reason               string                        `json:"reason,omitempty"`
	Message              string                        `json:"message,omitempty"`
	PropagationStatus    string                        `json:"propagation_status"`
	ServingReplicas      int                           `json:"serving_replicas"`
	AcknowledgedReplicas int                           `json:"acknowledged_replicas"`
	GatewayEvidence      []GrantGatewayReplicaEvidence `json:"gateway_evidence,omitempty"`
	LastTransitionAt     time.Time                     `json:"last_transition_at"`
	LastVerifiedAt       time.Time                     `json:"last_verified_at"`
	UpdatedAt            time.Time                     `json:"updated_at"`
}

// GrantIntent is Platform API's authoritative desired state for one grant
// incarnation, paired with its latest execution receipt, independent
// observation, and separated gateway propagation milestones.
type GrantIntent struct {
	GrantID               string                  `json:"grant_id"`
	Namespace             string                  `json:"namespace"`
	Name                  string                  `json:"name"`
	TeamID                string                  `json:"team_id"`
	ServerName            string                  `json:"server_name"`
	ServerUID             string                  `json:"server_uid"`
	Subject               GrantSubjectSummary     `json:"subject"`
	SpecJSON              json.RawMessage         `json:"spec_json"`
	DesiredRevisionID     string                  `json:"desired_revision_id"`
	State                 string                  `json:"state"`
	RevocationCommittedAt *time.Time              `json:"revocation_committed_at,omitempty"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	Receipt               *GrantReceipt           `json:"receipt,omitempty"`
	Observation           *GrantObservation       `json:"observation,omitempty"`
	GatewayPropagation    GrantGatewayPropagation `json:"gateway_propagation"`
}

// GrantIntentListResponse is GET /internal/access-intents/grants.
type GrantIntentListResponse struct {
	Grants []GrantIntent `json:"grants"`
}

// GrantIntentUpsertRequest is POST /internal/access-intents/grants.
type GrantIntentUpsertRequest struct {
	GrantID    string              `json:"grant_id,omitempty"`
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	TeamID     string              `json:"team_id"`
	ServerName string              `json:"server_name"`
	ServerUID  string              `json:"server_uid"`
	Subject    GrantSubjectSummary `json:"subject"`
	SpecJSON   json.RawMessage     `json:"spec_json"`
}

// GrantIntentRevokeRequest is an optional body for DELETE /internal/access-intents/grants/{grant_id}.
type GrantIntentRevokeRequest struct {
	Delete bool   `json:"delete,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// GrantIntentFenceRequest is POST /internal/access-intents/grants/{grant_id}/fence.
type GrantIntentFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// GrantIntentFenceResponse is POST /internal/access-intents/grants/{grant_id}/fence.
type GrantIntentFenceResponse struct {
	FenceEpoch int64       `json:"fence_epoch"`
	Intent     GrantIntent `json:"intent"`
}

// GrantIntentReceiptRequest is PUT /internal/access-intents/grants/{grant_id}/receipt.
type GrantIntentReceiptRequest struct {
	DesiredRevisionID string `json:"desired_revision_id"`
	FenceEpoch        int64  `json:"fence_epoch"`
	Outcome           string `json:"outcome"`
	TargetUID         string `json:"target_uid,omitempty"`
	TargetGeneration  int64  `json:"target_generation,omitempty"`
	SpecDigest        string `json:"spec_digest,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	WorkerID          string `json:"worker_id"`
	LastErrorClass    string `json:"last_error_class,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

// GrantIntentObservationRequest is PUT /internal/access-intents/grants/{grant_id}/observation.
type GrantIntentObservationRequest struct {
	ClusterKey           string                        `json:"cluster_key,omitempty"`
	ObserverID           string                        `json:"observer_id"`
	ObserverEpoch        int64                         `json:"observer_epoch"`
	ObservedRevisionID   string                        `json:"observed_revision_id,omitempty"`
	TargetUID            string                        `json:"target_uid,omitempty"`
	Health               string                        `json:"health"`
	Reason               string                        `json:"reason,omitempty"`
	Message              string                        `json:"message,omitempty"`
	PropagationStatus    string                        `json:"propagation_status"`
	ServingReplicas      int                           `json:"serving_replicas"`
	AcknowledgedReplicas int                           `json:"acknowledged_replicas"`
	GatewayEvidence      []GrantGatewayReplicaEvidence `json:"gateway_evidence,omitempty"`
	VerifiedAt           time.Time                     `json:"verified_at,omitempty"`
}

// EnrollmentReceipt records bounded execution progress for one adapter
// enrollment operation without persisting keys or raw certificate secrets.
type EnrollmentReceipt struct {
	FenceEpoch             int64      `json:"fence_epoch"`
	FenceHolder            string     `json:"fence_holder,omitempty"`
	AppliedRevisionID      string     `json:"applied_revision_id,omitempty"`
	CertificateRequestName string     `json:"certificate_request_name,omitempty"`
	CertificateRequestUID  string     `json:"certificate_request_uid,omitempty"`
	Outcome                string     `json:"outcome,omitempty"`
	Attempt                int        `json:"attempt,omitempty"`
	WorkerID               string     `json:"worker_id,omitempty"`
	LastErrorClass         string     `json:"last_error_class,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	AppliedAt              *time.Time `json:"applied_at,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// EnrollmentObservation records independently observed CertificateRequest state
// for one adapter enrollment operation.
type EnrollmentObservation struct {
	ObserverID            string    `json:"observer_id,omitempty"`
	ObserverEpoch         int64     `json:"observer_epoch"`
	ObservedRevisionID    string    `json:"observed_revision_id,omitempty"`
	CertificateRequestUID string    `json:"certificate_request_uid,omitempty"`
	Health                string    `json:"health"`
	Reason                string    `json:"reason,omitempty"`
	Message               string    `json:"message,omitempty"`
	LastTransitionAt      time.Time `json:"last_transition_at"`
	LastVerifiedAt        time.Time `json:"last_verified_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// EnrollmentIntent is Platform API's durable record for one adapter
// certificate enrollment operation, keyed idempotently to
// (SessionUID, ServerUID, CSRSHA256). Private keys are never stored.
type EnrollmentIntent struct {
	OperationID            string                 `json:"operation_id"`
	SessionUID             string                 `json:"session_uid"`
	ServerUID              string                 `json:"server_uid"`
	AgentID                string                 `json:"agent_id,omitempty"`
	Namespace              string                 `json:"namespace"`
	SessionName            string                 `json:"session_name"`
	CSRSHA256              string                 `json:"csr_sha256"`
	DesiredRevisionID      string                 `json:"desired_revision_id"`
	State                  string                 `json:"state"`
	CertificateRequestName string                 `json:"certificate_request_name"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
	ActivatedAt            *time.Time             `json:"activated_at,omitempty"`
	Receipt                *EnrollmentReceipt     `json:"receipt,omitempty"`
	Observation            *EnrollmentObservation `json:"observation,omitempty"`
}

// EnrollmentIntentListResponse is GET /internal/access-intents/enrollments.
type EnrollmentIntentListResponse struct {
	Enrollments []EnrollmentIntent `json:"enrollments"`
}

// EnrollmentIntentCreateRequest is POST /internal/access-intents/enrollments.
type EnrollmentIntentCreateRequest struct {
	SessionUID  string `json:"session_uid"`
	ServerUID   string `json:"server_uid"`
	AgentID     string `json:"agent_id,omitempty"`
	Namespace   string `json:"namespace"`
	SessionName string `json:"session_name"`
	CSRSHA256   string `json:"csr_sha256"`
}

// EnrollmentIntentFenceRequest is POST /internal/access-intents/enrollments/{op_id}/fence.
type EnrollmentIntentFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// EnrollmentIntentFenceResponse is POST /internal/access-intents/enrollments/{op_id}/fence.
type EnrollmentIntentFenceResponse struct {
	FenceEpoch int64            `json:"fence_epoch"`
	Intent     EnrollmentIntent `json:"intent"`
}

// EnrollmentIntentReceiptRequest is PUT /internal/access-intents/enrollments/{op_id}/receipt.
type EnrollmentIntentReceiptRequest struct {
	DesiredRevisionID      string `json:"desired_revision_id"`
	FenceEpoch             int64  `json:"fence_epoch"`
	State                  string `json:"state,omitempty"`
	Outcome                string `json:"outcome"`
	CertificateRequestName string `json:"certificate_request_name,omitempty"`
	CertificateRequestUID  string `json:"certificate_request_uid,omitempty"`
	Attempt                int    `json:"attempt,omitempty"`
	WorkerID               string `json:"worker_id"`
	LastErrorClass         string `json:"last_error_class,omitempty"`
	LastError              string `json:"last_error,omitempty"`
}

// EnrollmentIntentActivateRequest is POST /internal/access-intents/enrollments/{op_id}/activate.
type EnrollmentIntentActivateRequest struct {
	DesiredRevisionID      string `json:"desired_revision_id"`
	FenceEpoch             int64  `json:"fence_epoch"`
	WorkerID               string `json:"worker_id"`
	CertificateRequestName string `json:"certificate_request_name,omitempty"`
	CertificateRequestUID  string `json:"certificate_request_uid,omitempty"`
	Serial                 string `json:"serial,omitempty"`
	Fingerprint            string `json:"fingerprint,omitempty"`
}

// AgentCleanupReceipt records fenced execution progress for post-deactivation
// follow-up reconciliation after an agent has already been atomically
// deactivated in PostgreSQL.
type AgentCleanupReceipt struct {
	FenceEpoch        int64      `json:"fence_epoch"`
	FenceHolder       string     `json:"fence_holder,omitempty"`
	AppliedRevisionID string     `json:"applied_revision_id,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	Attempt           int        `json:"attempt,omitempty"`
	WorkerID          string     `json:"worker_id,omitempty"`
	LastErrorClass    string     `json:"last_error_class,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	AppliedAt         *time.Time `json:"applied_at,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// AgentCleanupIntent records the durable post-deactivation follow-up work
// committed atomically alongside agent deactivation and session revocation.
type AgentCleanupIntent struct {
	AgentID              string               `json:"agent_id"`
	TeamID               string               `json:"team_id"`
	DesiredRevisionID    string               `json:"desired_revision_id"`
	DeactivatedAt        time.Time            `json:"deactivated_at"`
	RevokedSessionsCount int64                `json:"revoked_sessions_count"`
	UpdatedAt            time.Time            `json:"updated_at"`
	Receipt              *AgentCleanupReceipt `json:"receipt,omitempty"`
}

// AgentCleanupFenceRequest is POST /internal/access-intents/agent-cleanups/{agent_id}/fence.
type AgentCleanupFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// AgentCleanupFenceResponse is POST /internal/access-intents/agent-cleanups/{agent_id}/fence.
type AgentCleanupFenceResponse struct {
	FenceEpoch int64              `json:"fence_epoch"`
	Intent     AgentCleanupIntent `json:"intent"`
}

// AgentCleanupReceiptRequest is PUT /internal/access-intents/agent-cleanups/{agent_id}/receipt.
type AgentCleanupReceiptRequest struct {
	DesiredRevisionID string `json:"desired_revision_id"`
	FenceEpoch        int64  `json:"fence_epoch"`
	Outcome           string `json:"outcome"`
	Attempt           int    `json:"attempt,omitempty"`
	WorkerID          string `json:"worker_id"`
	LastErrorClass    string `json:"last_error_class,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

// AccessIntentObserverLeaseRequest is POST /internal/access-intents/observer-lease.
type AccessIntentObserverLeaseRequest struct {
	ClusterKey string `json:"cluster_key,omitempty"`
	ObserverID string `json:"observer_id"`
}

// AccessIntentObserverLeaseResponse is POST /internal/access-intents/observer-lease.
type AccessIntentObserverLeaseResponse struct {
	ClusterKey    string `json:"cluster_key"`
	ObserverID    string `json:"observer_id"`
	ObserverEpoch int64  `json:"observer_epoch"`
}
