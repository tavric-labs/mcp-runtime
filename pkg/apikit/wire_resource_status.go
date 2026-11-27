package apikit

import "time"

// ResourceStatus is a public observation of current desired state. Operation
// completion is separate; LastVerifiedAt lets callers display its freshness.
type ResourceStatus struct {
	DesiredRevisionID  string     `json:"desired_revision_id,omitempty"`
	AppliedRevisionID  string     `json:"applied_revision_id,omitempty"`
	ObservedRevisionID string     `json:"observed_revision_id,omitempty"`
	Health             string     `json:"health"`
	Reason             string     `json:"reason"`
	LastVerifiedAt     *time.Time `json:"last_verified_at,omitempty"`
}
