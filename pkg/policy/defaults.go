package policy

import "mcp-runtime/pkg/identity"

// Policy enum values are independent from the defaults below.
const (
	ModeAllowList    = "allow-list"
	ModeObserve      = "observe"
	DecisionDeny     = "deny"
	DecisionAllow    = "allow"
	DefaultMode      = ModeAllowList
	DefaultDecision  = DecisionDeny
	DefaultEnforceOn = "call_tool"
	// DefaultVersion is a governance policy label, not an MCP protocol revision.
	DefaultVersion         = "v1"
	ObservabilityMode      = ModeObserve
	ObservabilityDecision  = DecisionAllow
	DefaultSessionStore    = "platform"
	DefaultSessionMaxLife  = "24h"
	DefaultSessionIdleTime = "1h"
	DefaultSessionUpstream = identity.AuthTokenHeader
)

// ConfigMapName is the rendered policy ConfigMap for a managed server.
func ConfigMapName(serverName string) string { return serverName + "-gateway-policy" }
