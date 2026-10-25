package events

// MCPRequest is the platform's audit payload for one gateway HTTP exchange.
// RPCMethod is open to core and extension method names. This is an observation
// contract, not an MCP protocol message or an implementation of an extension.
// Deliberately exclude request params, IDs, tokens, queries and raw bodies.
type MCPRequest struct {
	ServerUID               string `json:"server_uid"`
	WouldDeny               bool   `json:"would_deny,omitempty"`
	WouldDenyReason         string `json:"would_deny_reason,omitempty"`
	Idempotency             string `json:"idempotency,omitempty"`
	IdempotencyKeyHash      string `json:"idempotency_key_hash,omitempty"`
	Method                  string `json:"method"`
	Path                    string `json:"path"`
	Status                  int    `json:"status"`
	LatencyMS               int64  `json:"latency_ms"`
	BytesIn                 int64  `json:"bytes_in"`
	BytesOut                int    `json:"bytes_out"`
	Server                  string `json:"server"`
	Namespace               string `json:"namespace"`
	TeamID                  string `json:"team_id"`
	ResourceTeamID          string `json:"resource_team_id"`
	Cluster                 string `json:"cluster"`
	HumanID                 string `json:"human_id"`
	AgentID                 string `json:"agent_id"`
	SubjectTeamID           string `json:"subject_team_id"`
	SessionID               string `json:"session_id"`
	Decision                string `json:"decision"`
	Reason                  string `json:"reason"`
	PolicyVersion           string `json:"policy_version"`
	RPCMethod               string `json:"rpc_method,omitempty"`
	RPCMessageKind          string `json:"rpc_message_kind,omitempty"`
	ToolName                string `json:"tool_name,omitempty"`
	AuthDelegation          string `json:"auth_delegation,omitempty"`
	CallerIdentity          string `json:"caller_identity,omitempty"`
	MatchedGrant            string `json:"matched_grant,omitempty"`
	MatchedGrantNamespace   string `json:"matched_grant_namespace,omitempty"`
	MatchedSession          string `json:"matched_session,omitempty"`
	MatchedSessionNamespace string `json:"matched_session_namespace,omitempty"`
	RequiredTrust           string `json:"required_trust,omitempty"`
	RequiredSideEffect      string `json:"required_side_effect,omitempty"`
	RiskLevel               string `json:"risk_level,omitempty"`
	AdminTrust              string `json:"admin_trust,omitempty"`
	ConsentedTrust          string `json:"consented_trust,omitempty"`
	EffectiveTrust          string `json:"effective_trust,omitempty"`
}
