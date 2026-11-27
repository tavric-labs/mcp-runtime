package apikit

import "time"

// Operation describes one accepted bounded execution step. Success does not
// imply that the resulting resource is currently Ready.
type Operation struct {
	ID                string    `json:"id"`
	Kind              string    `json:"kind"`
	ResourceID        string    `json:"resource_id"`
	Namespace         string    `json:"namespace"`
	DesiredRevisionID string    `json:"desired_revision_id"`
	State             string    `json:"state"`
	ErrorClass        string    `json:"error_class,omitempty"`
	RequestID         string    `json:"request_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

const (
	OperationTeamCreate = "team.create"
	OperationTeamDelete = "team.delete"

	OperationPending    = "Pending"
	OperationRunning    = "Running"
	OperationSucceeded  = "Succeeded"
	OperationFailed     = "Failed"
	OperationSuperseded = "Superseded"
)
