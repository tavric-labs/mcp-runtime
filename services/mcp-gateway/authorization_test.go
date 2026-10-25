package main

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func authorizationExample() authorizationInput {
	return authorizationInput{
		Policy:               testPolicy(),
		Inspection:           inspectedRPC("tools/call", "echo", RPCRequest),
		Identity:             identityContext{HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme", SessionID: "session-1"},
		SessionSyncReady:     true,
		DefaultPolicyVersion: "fallback",
	}
}

func TestAuthorizationRequiresGrantAndConsent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, reason string
		change       func(*authorizationInput)
		status       int
	}{
		{name: "granted read", reason: "allowed", status: http.StatusOK, change: func(*authorizationInput) {}},
		{name: "unknown caller", reason: "missing_identity", status: http.StatusUnauthorized, change: func(in *authorizationInput) { in.Identity = identityContext{} }},
		{name: "different grant subject", reason: "no_matching_grant", status: http.StatusForbidden, change: func(in *authorizationInput) { in.Policy.Grants[0].HumanID = "someone-else" }},
		{name: "explicit tool denial", reason: "tool_denied", status: http.StatusForbidden, change: func(in *authorizationInput) { in.Policy.Grants[0].ToolRules[0].Decision = "deny" }},
		{name: "unconsented write", reason: "side_effect_not_allowed", status: http.StatusForbidden, change: func(in *authorizationInput) { in.Policy.Tools[0].SideEffect = "write" }},
		{name: "insufficient session trust", reason: "trust_too_low", status: http.StatusForbidden, change: func(in *authorizationInput) {
			in.Policy.Tools[0].RequiredTrust = "high"
			in.Policy.Sessions[0].ConsentedTrust = "low"
		}},
		{name: "revoked session", reason: "session_revoked", status: http.StatusUnauthorized, change: func(in *authorizationInput) { in.Policy.Sessions[0].Revoked = true }},
		{name: "missing policy", reason: "policy_unavailable", status: http.StatusServiceUnavailable, change: func(in *authorizationInput) { in.Policy = nil; in.PolicyErr = errors.New("not loaded") }},
		{name: "unparseable call", reason: "rpc_inspection_failed", status: http.StatusForbidden, change: func(in *authorizationInput) { in.Inspection = failedRPCInspection(inspectionMalformed, true) }},
		{name: "non tool traffic", reason: "allowed", status: http.StatusOK, change: func(in *authorizationInput) {
			in.Inspection = inspectedRPC("tools/list", "", RPCRequest)
			in.Identity = identityContext{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := authorizationExample()
			tc.change(&in)
			got := authorizeRequest(in, now)
			if got.Reason != tc.reason || got.Status != tc.status || got.Allowed != (tc.status == http.StatusOK) {
				t.Fatalf("decision=%+v, want %s (%d)", got, tc.reason, tc.status)
			}
			if tc.name == "granted read" && (got.MatchedGrant != "grant-1" || got.MatchedSession != "session-1") {
				t.Fatalf("authorization lost grant/session attribution: %+v", got)
			}
			if in.Policy == nil && got.PolicyVersion != "fallback" {
				t.Fatalf("missing policy version fallback: %+v", got)
			}
		})
	}
}

func TestAuthorizationExpiresGrantsAndSessionsAtTheirDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, resource := range []string{"grant", "session"} {
		for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			t.Run(resource+"/"+offset.String(), func(t *testing.T) {
				t.Parallel()
				in := authorizationExample()
				if resource == "grant" {
					in.Policy.Grants[0].ExpiresAt = deadline.Format(time.RFC3339)
				} else {
					in.Policy.Sessions[0].ExpiresAt = deadline.Format(time.RFC3339)
				}
				got := authorizeRequest(in, deadline.Add(offset))
				if offset < 0 {
					if !got.Allowed {
						t.Fatalf("denied before expiry: %+v", got)
					}
				} else if got.Allowed || got.Reason != resource+"_expired" {
					t.Fatalf("expired %s accepted or misclassified: %+v", resource, got)
				}
			})
		}
	}
}

func TestAuthorizationRejectsStaleSessionsExceptInObserveMode(t *testing.T) {
	t.Parallel()
	for _, observe := range []bool{false, true} {
		in := authorizationExample()
		in.Policy.Session.Store = "platform"
		in.SessionSyncReady = false
		if observe {
			in.Policy.Policy.Mode = "observe"
		}
		got := authorizeRequest(in, time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC))
		if got.Allowed != observe || (!observe && (got.Reason != "policy_unavailable" || got.Status != http.StatusServiceUnavailable)) {
			t.Fatalf("observe=%v decision=%+v", observe, got)
		}
	}
}

func TestToolCallIsDeniedWhenGrantExpires(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := deadline.Add(-time.Nanosecond)
	s := minimalServer()
	s.now = func() time.Time { return now }
	ex := newTestExchange(http.MethodPost, "/mcp", "", nil)
	input := authorizationExample()
	ex.Policy, ex.Identity, ex.Inspection = input.Policy, input.Identity, input.Inspection
	ex.Policy.Grants[0].ExpiresAt = deadline.Format(time.RFC3339)
	allowed, response := s.authzFilter(ex.AuthenticatedRequest)
	if response != nil || !allowed.Decision.Allowed {
		t.Fatal("valid grant denied before expiry")
	}
	now = deadline
	_, response = s.authzFilter(ex.AuthenticatedRequest)
	ex.record(response)
	if response == nil || response.Decision.Reason != "grant_expired" || ex.W.status != http.StatusForbidden {
		t.Fatalf("call at expiry: response=%+v status=%d", response, ex.W.status)
	}
}
