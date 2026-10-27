package main

import (
	"net/http"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// authzFilter consumes a complete identity and the captured policy snapshot.
// Non-tool traffic gets an explicit allow; tool calls evaluate grants/sessions.
func (s *gatewayServer) authzFilter(in AuthenticatedRequest) (AuthorizedRequest, *FilterResponse) {
	now := s.currentTime()
	ex := AuthorizedRequest{
		AuthenticatedRequest: in,
		Decision: authorizeRequest(authorizationInput{
			Policy: in.Policy, PolicyErr: in.PolicyErr,
			Inspection: in.Inspection, Identity: in.Identity,
			SessionSyncReady:     s.sessionReader().Fresh(now),
			DefaultPolicyVersion: s.defaultPolicyVersion,
		}, now),
	}
	if !ex.Decision.Allowed {
		return ex, denyResponse(ex.Decision)
	}
	return ex, nil
}

// authorizationInput contains facts, not HTTP state or live dependencies.
// The caller captures cache readiness and time before evaluating the decision.
type authorizationInput struct {
	Policy               *policypkg.Document
	PolicyErr            error
	Inspection           rpcInspection
	Identity             identityContext
	SessionSyncReady     bool
	DefaultPolicyVersion string
}

func authorizeRequest(in authorizationInput, now time.Time) policypkg.Decision {
	if !in.Inspection.RequiresAuthorization() {
		// Non-tool traffic intentionally bypasses grant/session evaluation.
		return policypkg.Allow(
			"allowed",
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(in.Policy), in.DefaultPolicyVersion),
		)
	}

	switch {
	case in.Policy != nil && in.Policy.Session != nil && in.Policy.Session.Store == "platform" &&
		in.Policy.Policy != nil && in.Policy.Policy.Mode != "observe" && !in.SessionSyncReady:
		return policypkg.Deny(http.StatusServiceUnavailable, "policy_unavailable", policypkg.PolicyVersion(in.Policy))
	case in.PolicyErr != nil:
		return policypkg.Deny(
			http.StatusServiceUnavailable,
			"policy_unavailable",
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(in.Policy), in.DefaultPolicyVersion),
		)
	case in.Inspection.Indeterminate():
		return policypkg.Deny(
			http.StatusForbidden,
			policypkg.FirstNonEmpty(in.Inspection.FailureReason(), "rpc_inspection_failed"),
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(in.Policy), in.DefaultPolicyVersion),
		)
	default:
		return policypkg.Authorize(in.Policy, policypkg.Request{
			Identity:  policyIdentity(in.Identity),
			RPCMethod: in.Inspection.Method(),
			ToolName:  policypkg.ToolName(in.Inspection.ToolName()),
			Session:   in.Identity.Session,
		}, now)
	}
}
