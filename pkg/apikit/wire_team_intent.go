package apikit

import "time"

// TeamIntentHealth values describe observed Kubernetes namespace health
// independently from River job execution state.
const (
	TeamIntentHealthPending     = "Pending"
	TeamIntentHealthProgressing = "Progressing"
	TeamIntentHealthReady       = "Ready"
	TeamIntentHealthDegraded    = "Degraded"
	TeamIntentHealthDeleted     = "Deleted"
	TeamIntentHealthUnknown     = "Unknown"
)

// TeamIntentOutcome values describe bounded execution receipts recorded by
// runtime workers for a specific team incarnation and desired revision.
const (
	TeamIntentOutcomeApplied    = "Applied"
	TeamIntentOutcomeCleaned    = "Cleaned"
	TeamIntentOutcomeSuperseded = "Superseded"
	TeamIntentOutcomeFailed     = "Failed"
	TeamIntentOutcomeTerminal   = "Terminal"
)

// TeamIntentErrorClass values classify worker reconciliation failures.
const (
	TeamIntentErrorTransient         = "transient"
	TeamIntentErrorOwnershipConflict = "ownership_conflict"
	TeamIntentErrorInvalidConfig     = "invalid_config"
	TeamIntentErrorExhausted         = "exhausted"
)

// TeamIntentCondition is one platform-facing condition observed on a managed
// team namespace.
type TeamIntentCondition struct {
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
	Message          string    `json:"message,omitempty"`
	RevisionID       string    `json:"revision_id,omitempty"`
	LastTransitionAt time.Time `json:"last_transition_at,omitempty"`
}

// TeamNamespaceReceipt records the latest fenced execution state written by a
// runtime worker. It records bounded step execution, not ongoing health.
type TeamNamespaceReceipt struct {
	FenceEpoch        int64      `json:"fence_epoch"`
	FenceHolder       string     `json:"fence_holder,omitempty"`
	AppliedRevisionID string     `json:"applied_revision_id,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	NamespaceUID      string     `json:"namespace_uid,omitempty"`
	Attempt           int        `json:"attempt,omitempty"`
	WorkerID          string     `json:"worker_id,omitempty"`
	LastErrorClass    string     `json:"last_error_class,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	AppliedAt         *time.Time `json:"applied_at,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// TeamNamespaceObservation records the latest independently observed
// Kubernetes state for a managed team incarnation.
type TeamNamespaceObservation struct {
	ObserverID         string                `json:"observer_id,omitempty"`
	ObserverEpoch      int64                 `json:"observer_epoch"`
	ObservedRevisionID string                `json:"observed_revision_id,omitempty"`
	NamespaceUID       string                `json:"namespace_uid,omitempty"`
	Health             string                `json:"health"`
	Reason             string                `json:"reason,omitempty"`
	Message            string                `json:"message,omitempty"`
	Conditions         []TeamIntentCondition `json:"conditions,omitempty"`
	LastTransitionAt   time.Time             `json:"last_transition_at"`
	LastVerifiedAt     time.Time             `json:"last_verified_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
}

// TeamNamespaceIntent is Platform API's authoritative desired state for one
// team incarnation, paired with its latest execution receipt and observation.
type TeamNamespaceIntent struct {
	TeamID              string                    `json:"team_id"`
	Slug                string                    `json:"slug"`
	Name                string                    `json:"name"`
	Namespace           string                    `json:"namespace"`
	DesiredRevisionID   string                    `json:"desired_revision_id"`
	Deleted             bool                      `json:"deleted"`
	UpdatedAt           time.Time                 `json:"updated_at"`
	ActiveOwnerTeamID   string                    `json:"active_owner_team_id,omitempty"`
	PriorDeletedTeamIDs []string                  `json:"prior_deleted_team_ids,omitempty"`
	Receipt             *TeamNamespaceReceipt     `json:"receipt,omitempty"`
	Observation         *TeamNamespaceObservation `json:"observation,omitempty"`
}

// TeamNamespaceIntentListResponse is GET /internal/team-intents.
type TeamNamespaceIntentListResponse struct {
	Intents []TeamNamespaceIntent `json:"intents"`
}

// TeamIntentFenceRequest is POST /internal/team-intents/{team_id}/fence.
type TeamIntentFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// TeamIntentFenceResponse is POST /internal/team-intents/{team_id}/fence.
type TeamIntentFenceResponse struct {
	FenceEpoch int64               `json:"fence_epoch"`
	Intent     TeamNamespaceIntent `json:"intent"`
}

// TeamIntentReceiptRequest is PUT /internal/team-intents/{team_id}/receipt.
type TeamIntentReceiptRequest struct {
	DesiredRevisionID string `json:"desired_revision_id"`
	FenceEpoch        int64  `json:"fence_epoch"`
	Outcome           string `json:"outcome"`
	NamespaceUID      string `json:"namespace_uid,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	WorkerID          string `json:"worker_id"`
	LastErrorClass    string `json:"last_error_class,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

// TeamIntentObserverLeaseRequest is POST /internal/team-intents/observer-lease.
type TeamIntentObserverLeaseRequest struct {
	ClusterKey string `json:"cluster_key,omitempty"`
	ObserverID string `json:"observer_id"`
}

// TeamIntentObserverLeaseResponse is POST /internal/team-intents/observer-lease.
type TeamIntentObserverLeaseResponse struct {
	ClusterKey    string `json:"cluster_key"`
	ObserverID    string `json:"observer_id"`
	ObserverEpoch int64  `json:"observer_epoch"`
}

// TeamIntentObservationRequest is PUT /internal/team-intents/{team_id}/observation.
type TeamIntentObservationRequest struct {
	ClusterKey         string                `json:"cluster_key,omitempty"`
	ObserverID         string                `json:"observer_id"`
	ObserverEpoch      int64                 `json:"observer_epoch"`
	ObservedRevisionID string                `json:"observed_revision_id,omitempty"`
	NamespaceUID       string                `json:"namespace_uid,omitempty"`
	Health             string                `json:"health"`
	Reason             string                `json:"reason,omitempty"`
	Message            string                `json:"message,omitempty"`
	Conditions         []TeamIntentCondition `json:"conditions,omitempty"`
	VerifiedAt         time.Time             `json:"verified_at,omitempty"`
}
