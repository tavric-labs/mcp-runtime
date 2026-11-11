package audit

import (
	"testing"
)

func TestDeploymentReadinessRejectsStaleRollouts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"ready", `{"items":[{"metadata":{"name":"api","generation":3},"spec":{"replicas":2},"status":{"observedGeneration":3,"replicas":2,"readyReplicas":2,"availableReplicas":2,"updatedReplicas":2}}]}`, true},
		{"stale-generation", `{"items":[{"metadata":{"name":"api","generation":3},"spec":{"replicas":2},"status":{"observedGeneration":2,"replicas":2,"readyReplicas":2,"availableReplicas":2,"updatedReplicas":2}}]}`, false},
		{"old-replicas-still-ready", `{"items":[{"metadata":{"name":"api","generation":3},"spec":{"replicas":2},"status":{"observedGeneration":3,"replicas":2,"readyReplicas":2,"availableReplicas":2,"updatedReplicas":1}}]}`, false},
		{"unavailable", `{"items":[{"metadata":{"name":"api","generation":3},"spec":{"replicas":2},"status":{"observedGeneration":3,"replicas":2,"readyReplicas":1,"availableReplicas":1,"updatedReplicas":2,"unavailableReplicas":1}}]}`, false},
		{"empty", `{"items":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckDeploymentReadiness([]byte(tc.body)); (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestPolicyDenialCannotBeAnExpiredSessionOrCrash(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{
		{403, `{"error":"tool_not_granted"}`, true},
		{403, `{"error":"tool_denied"}`, true},
		{403, `{"error":"session_expired"}`, false},
		{500, `{"error":"tool_not_granted"}`, false},
		{200, `{"error":{"code":-32601,"message":"not found"}}`, false},
		{403, `<html>forbidden</html>`, false},
	} {
		if err := ValidatePolicyDenial(tc.status, []byte(tc.body)); (err == nil) != tc.ok {
			t.Fatalf("HTTP %d %s: err=%v", tc.status, tc.body, err)
		}
	}
}

func TestAuthorizationMetadataRequiresIssuerAndBothScopes(t *testing.T) {
	issuer := "https://auth.example.test/mcp-auth"
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"issuer":"https://auth.example.test/mcp-auth","scopes_supported":["tools:read","tools:write"]}`, true},
		{`{"issuer":"https://wrong.example.test","scopes_supported":["tools:read","tools:write"]}`, false},
		{`{"issuer":"https://auth.example.test/mcp-auth","scopes_supported":["tools:read"]}`, false},
	} {
		if err := CheckAuthorizationMetadata([]byte(tc.body), issuer); (err == nil) != tc.ok {
			t.Fatalf("err=%v", err)
		}
	}
}
