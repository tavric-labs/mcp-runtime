package access

import "encoding/json"

// GrantSummary provides a simplified view of a grant for UI display.
type GrantSummary struct {
	Mode               GrantMode        `json:"mode,omitempty"`
	Name               string           `json:"name"`
	Namespace          string           `json:"namespace"`
	ServerRef          ServerReference  `json:"serverRef"`
	Subject            SubjectRef       `json:"subject"`
	MaxTrust           TrustLevel       `json:"maxTrust"`
	AllowedSideEffects []ToolSideEffect `json:"allowedSideEffects,omitempty"`
	ExpiresAt          *Time            `json:"expiresAt,omitempty"`
	Disabled           bool             `json:"disabled"`
	Age                string           `json:"age"`
}

// SessionSummary provides a simplified view of a session for UI display.
type SessionSummary struct {
	Name            string          `json:"name"`
	Namespace       string          `json:"namespace"`
	ServerRef       ServerReference `json:"serverRef"`
	Subject         SubjectRef      `json:"subject"`
	ConsentedTrust  TrustLevel      `json:"consentedTrust"`
	Revoked         bool            `json:"revoked"`
	ExpiresAt       *Time           `json:"expiresAt,omitempty"`
	Age             string          `json:"age"`
	PolicyVersion   string          `json:"policyVersion,omitempty"`
	ServerUID       string          `json:"serverUID,omitempty"`
	GrantName       string          `json:"grantName,omitempty"`
	GrantNamespace  string          `json:"grantNamespace,omitempty"`
	RevokedAt       string          `json:"revokedAt,omitempty"`
	RevocationCause string          `json:"revocationCause,omitempty"`
}

// ToJSON converts a grant or session to JSON bytes.
func ToJSON(obj interface{}) ([]byte, error) {
	return json.MarshalIndent(obj, "", "  ")
}
