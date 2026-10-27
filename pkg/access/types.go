package access

import (
	policypkg "mcp-runtime/pkg/policy"
)

// ServerName identifies an MCPServer resource by name.
type ServerName string

// Namespace identifies a Kubernetes namespace that contains MCP runtime resources.
type Namespace string

// HumanID identifies an authenticated human principal.
type HumanID string

// AgentID identifies an authenticated agent principal.
type AgentID string

// TeamID identifies a stable platform team principal.
type TeamID string

// ServerReference identifies an MCPServer.
type ServerReference struct {
	Name      ServerName `json:"name"`
	Namespace Namespace  `json:"namespace,omitempty"`
}

// SubjectRef identifies the human and optional agent a grant or session applies to.
type SubjectRef struct {
	HumanID HumanID `json:"humanID,omitempty"`
	AgentID AgentID `json:"agentID,omitempty"`
	TeamID  TeamID  `json:"teamID,omitempty"`
}

// TrustLevel defines trust levels for access control.
type TrustLevel string

const (
	TrustLow    TrustLevel = "low"
	TrustMedium TrustLevel = "medium"
	TrustHigh   TrustLevel = "high"
)

// ToolSideEffect classifies whether a tool reads, mutates, or destructively changes state.
type ToolSideEffect string

const (
	SideEffectRead        ToolSideEffect = "read"
	SideEffectWrite       ToolSideEffect = "write"
	SideEffectDestructive ToolSideEffect = "destructive"
)

// PolicyDecision defines policy decisions for tool access.
type PolicyDecision string

const (
	DecisionAllow PolicyDecision = "allow"
	DecisionDeny  PolicyDecision = policypkg.DecisionDeny
	DecisionAudit PolicyDecision = "audit"
)

// ToolRule controls access to an individual MCP tool.
type ToolRule struct {
	Name          string         `json:"name"`
	Decision      PolicyDecision `json:"decision"`
	RequiredTrust TrustLevel     `json:"requiredTrust,omitempty"`
}

// SecretKeyRef references a secret key.
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// MCPAccessGrantSpec defines who can use which MCP server and with what trust ceiling.
type MCPAccessGrantSpec struct {
	Mode               GrantMode        `json:"mode,omitempty"`
	ServerRef          ServerReference  `json:"serverRef"`
	Subject            SubjectRef       `json:"subject"`
	MaxTrust           TrustLevel       `json:"maxTrust,omitempty"`
	AllowedSideEffects []ToolSideEffect `json:"allowedSideEffects,omitempty"`
	PolicyVersion      string           `json:"policyVersion,omitempty"`
	Disabled           bool             `json:"disabled,omitempty"`
	ExpiresAt          *Time            `json:"expiresAt,omitempty"`
	ToolRules          []ToolRule       `json:"toolRules,omitempty"`
}

// GrantMode controls whether a grant enforces or shadows its restrictions.
type GrantMode string

const (
	GrantModeEnforce GrantMode = "enforce"
	GrantModeMonitor GrantMode = "monitor"
)

// MCPAccessGrantStatus captures observed grant state.
type MCPAccessGrantStatus struct {
	Phase      string      `json:"phase,omitempty"`
	Message    string      `json:"message,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// MCPAgentSessionSpec defines a consented server-side agent session.
type MCPAgentSessionSpec struct {
	ServerRef              ServerReference `json:"serverRef"`
	Subject                SubjectRef      `json:"subject"`
	ConsentedTrust         TrustLevel      `json:"consentedTrust,omitempty"`
	ExpiresAt              *Time           `json:"expiresAt,omitempty"`
	Revoked                bool            `json:"revoked,omitempty"`
	UpstreamTokenSecretRef *SecretKeyRef   `json:"upstreamTokenSecretRef,omitempty"`
	PolicyVersion          string          `json:"policyVersion,omitempty"`
}

// MCPAgentSessionStatus captures observed session state.
type MCPAgentSessionStatus struct {
	Phase      string      `json:"phase,omitempty"`
	Message    string      `json:"message,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}
