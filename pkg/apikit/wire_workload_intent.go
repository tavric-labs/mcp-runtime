package apikit

import (
	"encoding/json"
	"time"
)

// WorkloadIntentHealth values describe independently observed Kubernetes
// workload health without coupling it to River job execution state.
const (
	WorkloadIntentHealthPending     = "Pending"
	WorkloadIntentHealthProgressing = "Progressing"
	WorkloadIntentHealthReady       = "Ready"
	WorkloadIntentHealthDegraded    = "Degraded"
	WorkloadIntentHealthDeleted     = "Deleted"
	WorkloadIntentHealthUnknown     = "Unknown"
)

// WorkloadIntentOutcome values describe bounded execution receipts recorded by
// runtime workers for a specific desired revision and execution fence.
const (
	WorkloadIntentOutcomeApplied           = "Applied"
	WorkloadIntentOutcomeCleaned           = "Cleaned"
	WorkloadIntentOutcomeSuperseded        = "Superseded"
	WorkloadIntentOutcomeFailed            = "Failed"
	WorkloadIntentOutcomeTerminal          = "Terminal"
	WorkloadIntentOutcomeBlockedDependency = "BlockedDependency"
)

// WorkloadIntentErrorClass values classify worker reconciliation failures.
const (
	WorkloadIntentErrorTransient         = "transient"
	WorkloadIntentErrorOwnershipConflict = "ownership_conflict"
	WorkloadIntentErrorInvalidConfig     = "invalid_config"
	WorkloadIntentErrorDependencyPending = "dependency_pending"
	WorkloadIntentErrorExhausted         = "exhausted"
)

// ServerLifecycle values describe the durable lifecycle of an MCPServer intent.
const (
	ServerLifecycleActive   = "Active"
	ServerLifecycleRetiring = "Retiring"
	ServerLifecycleRetired  = "Retired"
)

// ImageUploadState values track durable staging upload progress prior to queue
// acceptance. Only Finalized uploads may be enqueued for registry publication.
const (
	ImageUploadStateUploading = "Uploading"
	ImageUploadStateFinalized = "Finalized"
	ImageUploadStateAborted   = "Aborted"
	ImageUploadStateExpired   = "Expired"
)

// ImagePublicationStatus values track the durable image publication lifecycle.
const (
	ImagePublicationStatusUploading  = "Uploading"
	ImagePublicationStatusQueued     = "Queued"
	ImagePublicationStatusPublishing = "Publishing"
	ImagePublicationStatusPublished  = "Published"
	ImagePublicationStatusFailed     = "Failed"
	ImagePublicationStatusAborted    = "Aborted"
)

// MutableTagPolicy values control concurrent publication overlap for the same
// registry/repository:tag target.
const (
	MutableTagPolicyRejectOverlap = "reject_overlap"
	MutableTagPolicyAllowOverlap  = "allow_overlap"
)

// RestartStatus values track durable imperative component restart operations.
const (
	RestartStatusQueued     = "Queued"
	RestartStatusInProgress = "InProgress"
	RestartStatusCompleted  = "Completed"
	RestartStatusFailed     = "Failed"
)

// WorkloadIntentCondition is one platform-facing condition observed on a
// managed Kubernetes workload.
type WorkloadIntentCondition struct {
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
	Message          string    `json:"message,omitempty"`
	RevisionID       string    `json:"revision_id,omitempty"`
	LastTransitionAt time.Time `json:"last_transition_at,omitempty"`
}

// WorkloadExecutionReceipt records the latest fenced execution state written by
// a runtime worker. It tracks bounded step execution, never ongoing health.
type WorkloadExecutionReceipt struct {
	FenceEpoch        int64      `json:"fence_epoch"`
	FenceHolder       string     `json:"fence_holder,omitempty"`
	AppliedRevisionID string     `json:"applied_revision_id,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	TargetUID         string     `json:"target_uid,omitempty"`
	TargetGeneration  int64      `json:"target_generation,omitempty"`
	SpecDigest        string     `json:"spec_digest,omitempty"`
	Attempt           int        `json:"attempt,omitempty"`
	WorkerID          string     `json:"worker_id,omitempty"`
	LastErrorClass    string     `json:"last_error_class,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	AppliedAt         *time.Time `json:"applied_at,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// WorkloadStatusObservation records independently observed Kubernetes workload
// state keyed by monotonic observer_epoch.
type WorkloadStatusObservation struct {
	ObserverID         string                    `json:"observer_id,omitempty"`
	ObserverEpoch      int64                     `json:"observer_epoch"`
	ObservedRevisionID string                    `json:"observed_revision_id,omitempty"`
	TargetUID          string                    `json:"target_uid,omitempty"`
	ObservedGeneration int64                     `json:"observed_generation,omitempty"`
	Health             string                    `json:"health"`
	Reason             string                    `json:"reason,omitempty"`
	Message            string                    `json:"message,omitempty"`
	Conditions         []WorkloadIntentCondition `json:"conditions,omitempty"`
	LastTransitionAt   time.Time                 `json:"last_transition_at"`
	LastVerifiedAt     time.Time                 `json:"last_verified_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
}

// ServerRetirementCheckpoints records durable ordered retirement progress so
// crash recovery resumes cleanly without repeating or reordering steps.
type ServerRetirementCheckpoints struct {
	RetiringMarked     bool `json:"retiring_marked"`
	SessionsFenced     bool `json:"sessions_fenced"`
	GrantsRetired      bool `json:"grants_retired"`
	CredentialsCleaned bool `json:"credentials_cleaned"`
	CRDeleted          bool `json:"cr_deleted"`
}

// ServerIntent is Platform API's authoritative desired state for one MCPServer,
// paired with ordered retirement checkpoints, execution receipt, and
// independent health observation.
type ServerIntent struct {
	ServerID              string                      `json:"server_id"`
	Namespace             string                      `json:"namespace"`
	Name                  string                      `json:"name"`
	TeamID                string                      `json:"team_id,omitempty"`
	DesiredRevisionID     string                      `json:"desired_revision_id"`
	Lifecycle             string                      `json:"lifecycle"`
	SpecJSON              json.RawMessage             `json:"spec_json,omitempty"`
	SpecDigest            string                      `json:"spec_digest,omitempty"`
	PublicationID         string                      `json:"publication_id,omitempty"`
	RetirementCheckpoints ServerRetirementCheckpoints `json:"retirement_checkpoints"`
	TargetCRUID           string                      `json:"target_cr_uid,omitempty"`
	UpdatedAt             time.Time                   `json:"updated_at"`
	Receipt               *WorkloadExecutionReceipt   `json:"receipt,omitempty"`
	Observation           *WorkloadStatusObservation  `json:"observation,omitempty"`
}

// DeploymentIntent is Platform API's authoritative desired state for a raw
// managed Kubernetes Deployment and Service (bypassing the MCPServer operator).
type DeploymentIntent struct {
	DeploymentID        string                     `json:"deployment_id"`
	Namespace           string                     `json:"namespace"`
	Name                string                     `json:"name"`
	TeamID              string                     `json:"team_id,omitempty"`
	DesiredRevisionID   string                     `json:"desired_revision_id"`
	Deleted             bool                       `json:"deleted"`
	Image               string                     `json:"image"`
	Version             string                     `json:"version,omitempty"`
	Port                int32                      `json:"port"`
	Replicas            int32                      `json:"replicas"`
	SpecDigest          string                     `json:"spec_digest,omitempty"`
	TargetDeploymentUID string                     `json:"target_deployment_uid,omitempty"`
	UpdatedAt           time.Time                  `json:"updated_at"`
	Receipt             *WorkloadExecutionReceipt  `json:"receipt,omitempty"`
	Observation         *WorkloadStatusObservation `json:"observation,omitempty"`
}

// ImagePublicationIntent is Platform API's authoritative state for a durable
// registry archive publication backed by staged immutable artifacts.
type ImagePublicationIntent struct {
	PublicationID     string                    `json:"publication_id"`
	TeamID            string                    `json:"team_id,omitempty"`
	Namespace         string                    `json:"namespace"`
	Registry          string                    `json:"registry"`
	Repository        string                    `json:"repository"`
	Tag               string                    `json:"tag"`
	MutableTagPolicy  string                    `json:"mutable_tag_policy"`
	UploadState       string                    `json:"upload_state"`
	ArtifactRef       string                    `json:"artifact_ref,omitempty"`
	ArtifactSHA256    string                    `json:"artifact_sha256,omitempty"`
	ArtifactSizeBytes int64                     `json:"artifact_size_bytes,omitempty"`
	HelperName        string                    `json:"helper_name,omitempty"`
	VerifiedDigest    string                    `json:"verified_digest,omitempty"`
	DesiredRevisionID string                    `json:"desired_revision_id"`
	Status            string                    `json:"status"`
	UpdatedAt         time.Time                 `json:"updated_at"`
	Receipt           *WorkloadExecutionReceipt `json:"receipt,omitempty"`
}

// ComponentRestartCheckpoint records durable completion of one component patch
// inside a restart operation.
type ComponentRestartCheckpoint struct {
	Component string     `json:"component"`
	Completed bool       `json:"completed"`
	PatchedAt *time.Time `json:"patched_at,omitempty"`
	TargetUID string     `json:"target_uid,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// RestartOperationIntent is Platform API's authoritative desired state for an
// imperative component restart using a fixed acceptance-time RestartedAt token.
type RestartOperationIntent struct {
	OperationID          string                                `json:"operation_id"`
	RequestedBy          string                                `json:"requested_by"`
	TargetComponents     []string                              `json:"target_components"`
	RestartedAt          string                                `json:"restarted_at"`
	ComponentCheckpoints map[string]ComponentRestartCheckpoint `json:"component_checkpoints,omitempty"`
	DesiredRevisionID    string                                `json:"desired_revision_id"`
	Status               string                                `json:"status"`
	UpdatedAt            time.Time                             `json:"updated_at"`
	Receipt              *WorkloadExecutionReceipt             `json:"receipt,omitempty"`
}

// WorkloadObserverLeaseRequest is POST /internal/workload-intents/observer-lease.
type WorkloadObserverLeaseRequest struct {
	ClusterKey string `json:"cluster_key,omitempty"`
	ObserverID string `json:"observer_id"`
}

// WorkloadObserverLeaseResponse is POST /internal/workload-intents/observer-lease.
type WorkloadObserverLeaseResponse struct {
	ClusterKey    string `json:"cluster_key"`
	ObserverID    string `json:"observer_id"`
	ObserverEpoch int64  `json:"observer_epoch"`
}

// ServerIntentUpsertRequest is POST /internal/workload-intents/servers.
type ServerIntentUpsertRequest struct {
	ServerID      string          `json:"server_id"`
	Namespace     string          `json:"namespace"`
	Name          string          `json:"name"`
	TeamID        string          `json:"team_id,omitempty"`
	SpecJSON      json.RawMessage `json:"spec_json"`
	SpecDigest    string          `json:"spec_digest,omitempty"`
	PublicationID string          `json:"publication_id,omitempty"`
}

// ServerIntentRetireRequest is POST /internal/workload-intents/servers/{server_id}/retire.
type ServerIntentRetireRequest struct {
	TargetCRUID string `json:"target_cr_uid,omitempty"`
}

// ServerIntentListResponse is GET /internal/workload-intents/servers.
type ServerIntentListResponse struct {
	Intents []ServerIntent `json:"intents"`
}

// ServerIntentFenceRequest is POST /internal/workload-intents/servers/{server_id}/fence.
type ServerIntentFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// ServerIntentFenceResponse is POST /internal/workload-intents/servers/{server_id}/fence.
type ServerIntentFenceResponse struct {
	FenceEpoch int64        `json:"fence_epoch"`
	Intent     ServerIntent `json:"intent"`
}

// ServerRetirementCheckpointRequest is PUT /internal/workload-intents/servers/{server_id}/checkpoint.
type ServerRetirementCheckpointRequest struct {
	DesiredRevisionID string                      `json:"desired_revision_id"`
	FenceEpoch        int64                       `json:"fence_epoch"`
	WorkerID          string                      `json:"worker_id"`
	TargetCRUID       string                      `json:"target_cr_uid,omitempty"`
	Checkpoints       ServerRetirementCheckpoints `json:"checkpoints"`
}

// ServerIntentReceiptRequest is PUT /internal/workload-intents/servers/{server_id}/receipt.
type ServerIntentReceiptRequest struct {
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

// ServerIntentObservationRequest is PUT /internal/workload-intents/servers/{server_id}/observation.
type ServerIntentObservationRequest struct {
	ClusterKey         string                    `json:"cluster_key,omitempty"`
	ObserverID         string                    `json:"observer_id"`
	ObserverEpoch      int64                     `json:"observer_epoch"`
	ObservedRevisionID string                    `json:"observed_revision_id,omitempty"`
	TargetUID          string                    `json:"target_uid,omitempty"`
	ObservedGeneration int64                     `json:"observed_generation,omitempty"`
	Health             string                    `json:"health"`
	Reason             string                    `json:"reason,omitempty"`
	Message            string                    `json:"message,omitempty"`
	Conditions         []WorkloadIntentCondition `json:"conditions,omitempty"`
	VerifiedAt         time.Time                 `json:"verified_at,omitempty"`
}

// DeploymentIntentUpsertRequest is POST /internal/workload-intents/deployments.
type DeploymentIntentUpsertRequest struct {
	DeploymentID string `json:"deployment_id"`
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	TeamID       string `json:"team_id,omitempty"`
	Image        string `json:"image"`
	Version      string `json:"version,omitempty"`
	Port         int32  `json:"port"`
	Replicas     int32  `json:"replicas"`
	SpecDigest   string `json:"spec_digest,omitempty"`
}

// DeploymentIntentDeleteRequest is POST /internal/workload-intents/deployments/{deployment_id}/delete.
type DeploymentIntentDeleteRequest struct {
	TargetDeploymentUID string `json:"target_deployment_uid,omitempty"`
}

// DeploymentIntentListResponse is GET /internal/workload-intents/deployments.
type DeploymentIntentListResponse struct {
	Intents []DeploymentIntent `json:"intents"`
}

// DeploymentIntentFenceRequest is POST /internal/workload-intents/deployments/{deployment_id}/fence.
type DeploymentIntentFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// DeploymentIntentFenceResponse is POST /internal/workload-intents/deployments/{deployment_id}/fence.
type DeploymentIntentFenceResponse struct {
	FenceEpoch int64            `json:"fence_epoch"`
	Intent     DeploymentIntent `json:"intent"`
}

// DeploymentIntentReceiptRequest is PUT /internal/workload-intents/deployments/{deployment_id}/receipt.
type DeploymentIntentReceiptRequest struct {
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

// DeploymentIntentObservationRequest is PUT /internal/workload-intents/deployments/{deployment_id}/observation.
type DeploymentIntentObservationRequest struct {
	ClusterKey         string                    `json:"cluster_key,omitempty"`
	ObserverID         string                    `json:"observer_id"`
	ObserverEpoch      int64                     `json:"observer_epoch"`
	ObservedRevisionID string                    `json:"observed_revision_id,omitempty"`
	TargetUID          string                    `json:"target_uid,omitempty"`
	ObservedGeneration int64                     `json:"observed_generation,omitempty"`
	Health             string                    `json:"health"`
	Reason             string                    `json:"reason,omitempty"`
	Message            string                    `json:"message,omitempty"`
	Conditions         []WorkloadIntentCondition `json:"conditions,omitempty"`
	VerifiedAt         time.Time                 `json:"verified_at,omitempty"`
}

// ImagePublicationBeginRequest is POST /internal/workload-intents/publications.
type ImagePublicationBeginRequest struct {
	PublicationID    string `json:"publication_id,omitempty"`
	TeamID           string `json:"team_id,omitempty"`
	Namespace        string `json:"namespace"`
	Registry         string `json:"registry"`
	Repository       string `json:"repository"`
	Tag              string `json:"tag"`
	MutableTagPolicy string `json:"mutable_tag_policy,omitempty"`
}

// ImagePublicationFinalizeRequest is POST /internal/workload-intents/publications/{publication_id}/finalize.
type ImagePublicationFinalizeRequest struct {
	ArtifactRef       string `json:"artifact_ref"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	ArtifactSizeBytes int64  `json:"artifact_size_bytes"`
}

// ImagePublicationAbortRequest is POST /internal/workload-intents/publications/{publication_id}/abort.
type ImagePublicationAbortRequest struct {
	UploadState string `json:"upload_state,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// ImagePublicationListResponse is GET /internal/workload-intents/publications.
type ImagePublicationListResponse struct {
	Intents []ImagePublicationIntent `json:"intents"`
}

// ImagePublicationFenceRequest is POST /internal/workload-intents/publications/{publication_id}/fence.
type ImagePublicationFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// ImagePublicationFenceResponse is POST /internal/workload-intents/publications/{publication_id}/fence.
type ImagePublicationFenceResponse struct {
	FenceEpoch int64                  `json:"fence_epoch"`
	Intent     ImagePublicationIntent `json:"intent"`
}

// ImagePublicationReceiptRequest is PUT /internal/workload-intents/publications/{publication_id}/receipt.
type ImagePublicationReceiptRequest struct {
	DesiredRevisionID string `json:"desired_revision_id"`
	FenceEpoch        int64  `json:"fence_epoch"`
	Outcome           string `json:"outcome"`
	Status            string `json:"status,omitempty"`
	HelperName        string `json:"helper_name,omitempty"`
	VerifiedDigest    string `json:"verified_digest,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	WorkerID          string `json:"worker_id"`
	LastErrorClass    string `json:"last_error_class,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

// RestartOperationCreateRequest is POST /internal/workload-intents/restarts.
type RestartOperationCreateRequest struct {
	OperationID      string   `json:"operation_id,omitempty"`
	RequestedBy      string   `json:"requested_by"`
	TargetComponents []string `json:"target_components"`
	RestartedAt      string   `json:"restarted_at,omitempty"`
}

// RestartOperationListResponse is GET /internal/workload-intents/restarts.
type RestartOperationListResponse struct {
	Intents []RestartOperationIntent `json:"intents"`
}

// RestartOperationFenceRequest is POST /internal/workload-intents/restarts/{operation_id}/fence.
type RestartOperationFenceRequest struct {
	WorkerID string `json:"worker_id"`
}

// RestartOperationFenceResponse is POST /internal/workload-intents/restarts/{operation_id}/fence.
type RestartOperationFenceResponse struct {
	FenceEpoch int64                  `json:"fence_epoch"`
	Intent     RestartOperationIntent `json:"intent"`
}

// RestartComponentCheckpointRequest is PUT /internal/workload-intents/restarts/{operation_id}/checkpoint.
type RestartComponentCheckpointRequest struct {
	DesiredRevisionID string                     `json:"desired_revision_id"`
	FenceEpoch        int64                      `json:"fence_epoch"`
	WorkerID          string                     `json:"worker_id"`
	Checkpoint        ComponentRestartCheckpoint `json:"checkpoint"`
}

// RestartOperationReceiptRequest is PUT /internal/workload-intents/restarts/{operation_id}/receipt.
type RestartOperationReceiptRequest struct {
	DesiredRevisionID string `json:"desired_revision_id"`
	FenceEpoch        int64  `json:"fence_epoch"`
	Outcome           string `json:"outcome"`
	Status            string `json:"status,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	WorkerID          string `json:"worker_id"`
	LastErrorClass    string `json:"last_error_class,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}
