package main

import policypkg "mcp-runtime/pkg/policy"

// policyFilter captures one policy snapshot. Public OAuth metadata terminates
// here, before authentication, with audit explicitly skipped.
func (s *gatewayServer) policyFilter(in InspectedRequest) (PolicyRequest, *FilterResponse) {
	ex := PolicyRequest{InspectedRequest: in}
	ex.Policy, ex.PolicyErr = s.currentPolicy()
	if metadata := protectedResourceMetadata(ex.R, ex.Policy); metadata != nil {
		// Public metadata response that bypasses auth/authz. Record an explicit
		// allow so request metrics reflect this 200 (audit is skipped); the deny
		// default would otherwise mislabel it.
		decision := policypkg.Allow(
			"oauth_metadata",
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
		)
		return ex, metadataResponse(decision, metadata)
	}
	return ex, nil
}
