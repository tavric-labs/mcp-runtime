package main

import (
	"net/http"
	"testing"

	policypkg "mcp-runtime/pkg/policy"
)

// verifiedTestCaller builds a certificate-verified caller for tests that need
// a populated identity without exercising authentication.
func verifiedTestCaller(actor identityContext) Caller { return certificateCaller(actor, "") }

func TestCallerConstructorsTagProvenance(t *testing.T) {
	t.Parallel()
	actor := identityContext{HumanID: "human", AgentID: "agent", TeamID: "team", SessionID: "session"}
	for _, tc := range []struct {
		name     string
		caller   Caller
		kind     CallerKind
		identity identityContext
		token    string
		usable   bool
	}{
		{"zero value is anonymous", Caller{}, CallerAnonymous, identityContext{}, "", true},
		{"anonymous", anonymousCaller(), CallerAnonymous, identityContext{}, "", true},
		{"delegated has no gateway identity", delegatedCaller(), CallerDelegated, identityContext{}, "", true},
		{"oauth", oauthCaller(actor, "bearer"), CallerOAuth, actor, "bearer", true},
		{"certificate", certificateCaller(actor, ""), CallerCertificate, actor, "", true},
		{"certificate and oauth", certificateCaller(actor, "bearer"), CallerCertificate, actor, "bearer", true},
		{"rejected keeps actor, drops token", rejectedCaller(actor), CallerRejected, actor, "", false},
	} {
		if tc.caller.Kind() != tc.kind || tc.caller.Identity() != tc.identity ||
			tc.caller.OAuthToken() != tc.token || tc.caller.Usable() != tc.usable {
			t.Errorf("%s: %+v", tc.name, tc.caller)
		}
	}
}

func TestAuthenticationReportsCallerProvenance(t *testing.T) {
	t.Parallel()
	s := minimalServer()

	anonymous := newTestExchange(http.MethodPost, "/mcp", "", nil)
	anonymous.Policy = testPolicy()
	if out, response := s.authFilter(anonymous.PolicyRequest); response != nil || out.Caller.Kind() != CallerAnonymous || out.Identity() != (identityContext{}) {
		t.Fatalf("anonymous: kind=%v response=%+v", out.Caller.Kind(), response)
	}

	delegated := newTestExchange(http.MethodPost, "/mcp", "", map[string]string{"X-Upstream-Token": "synthetic"})
	delegated.Policy = testPolicy()
	delegated.Policy.Auth = &policypkg.Auth{Mode: "header", Headers: []string{"X-Upstream-Token"}}
	out, response := s.authFilter(delegated.PolicyRequest)
	if response != nil || out.Caller.Kind() != CallerDelegated || out.Identity() != (identityContext{}) || out.OAuthToken() != "" {
		t.Fatalf("delegated: kind=%v identity=%+v response=%+v", out.Caller.Kind(), out.Identity(), response)
	}
}

// A revoked session keeps its verified actor for audit, but the caller is
// rejected, so it can never be authorized even if a stage were mis-ordered.
func TestRevokedSessionKeepsActorForAuditWithoutUsableCaller(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{"method":"tools/call","params":{"name":"echo"}}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = testPolicy()
	ex.Policy.Auth.TrustDomain = "example.org"
	ex.Policy.Sessions[0].Namespace = "team"
	ex.Policy.Sessions[0].Revoked = true
	ex.Inspection = inspectedRPC("tools/call", "echo", RPCRequest)
	out, response := s.authFilter(ex.PolicyRequest)
	if response == nil || response.Decision.Reason != "session_revoked" {
		t.Fatalf("response=%+v", response)
	}
	if out.Caller.Kind() != CallerRejected || out.Caller.Usable() || out.Identity().HumanID != "human-1" {
		t.Fatalf("caller=%+v", out.Caller)
	}
	evaluated, denial := s.authzFilter(out)
	if denial == nil || evaluated.Decision.Allowed {
		t.Fatalf("rejected caller reached authorization: %+v", evaluated.Decision)
	}
	if _, response := permitRequest(evaluated, ex.W); response == nil {
		t.Fatal("rejected caller was permitted to dispatch")
	}
}
