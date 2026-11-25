package main

import (
	"net/http"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

func TestPrimitiveMethodsRequireAuthorization(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"resources/read", "resources/list", "resources/subscribe", "prompts/get", "prompts/list", "skills/get", "skills/list"} {
		if !inspectedRPC(m, "", RPCRequest).RequiresAuthorization() {
			t.Errorf("%s must be evaluated against grants", m)
		}
	}
	for _, m := range []string{"initialize", "tools/list", "ping", "completion/complete", "notifications/initialized"} {
		if inspectedRPC(m, "", RPCRequest).RequiresAuthorization() {
			t.Errorf("%s must keep bypassing grant evaluation", m)
		}
	}
}

func TestResourceReadIsAuthorizedAgainstGrants(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	read := func(in *authorizationInput, uri string) {
		in.Inspection = inspectRPCBody([]byte(`{"id":1,"method":"resources/read","params":{"uri":"` + uri + `"}}`))
	}
	for _, tc := range []struct {
		name, reason string
		status       int
		change       func(*authorizationInput)
	}{
		{"granted", "allowed", http.StatusOK, func(in *authorizationInput) { read(in, "file:///docs/a.md") }},
		{"no matching grant", "no_matching_grant", http.StatusForbidden, func(in *authorizationInput) {
			read(in, "file:///docs/a.md")
			in.Policy.Grants[0].HumanID = "someone-else"
		}},
		{"anonymous", "missing_identity", http.StatusUnauthorized, func(in *authorizationInput) { read(in, "x"); in.Identity = identityContext{} }},
		{"revoked session", "session_revoked", http.StatusUnauthorized, func(in *authorizationInput) { read(in, "x"); in.Policy.Sessions[0].Revoked = true }},
		{"outside scoped rules", "primitive_not_granted", http.StatusForbidden, func(in *authorizationInput) {
			read(in, "file:///etc/passwd")
			in.Policy.Grants[0].PrimitiveRules = []policypkg.PrimitiveRule{{Kind: policypkg.PrimitiveResource, Pattern: "file:///docs/*", Decision: "allow"}}
		}},
		{"uri is read from params", "primitive_target_missing", http.StatusForbidden, func(in *authorizationInput) {
			in.Inspection = inspectRPCBody([]byte(`{"id":1,"method":"resources/read","params":{"name":"file:///docs/a.md"}}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := authorizationExample()
			tc.change(&in)
			got := authorizeRequest(in, now)
			if got.Reason != tc.reason || got.Status != tc.status {
				t.Fatalf("decision=%+v, want %s (%d)", got, tc.reason, tc.status)
			}
		})
	}
}
