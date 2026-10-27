package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// A static policy session binding is not parsed by policy validation, so a
// malformed expiry can reach validateSessionBinding through a validated
// document. The helper must fail closed, as pkg/policy's evaluator does.
func TestMalformedStaticSessionExpiryIsReachableAndFailsClosed(t *testing.T) {
	t.Parallel()
	doc := testPolicy()
	doc.Auth.TrustDomain = "example.org"
	doc.Sessions[0].Namespace = "team"
	doc.Sessions[0].ExpiresAt = "not-a-time"
	doc.Server = policypkg.Server{Name: "server", Namespace: "team"}
	if err := policypkg.Stamp(doc, ""); err != nil {
		t.Fatal(err)
	}
	if err := policypkg.Validate(doc); err != nil {
		t.Fatalf("policy validation now rejects a malformed session expiry (%v); the helper is no longer reachable with one, update this test", err)
	}
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", "", map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = doc
	_, response := s.authFilter(ex.PolicyRequest)
	if response == nil || response.Decision.Reason != "session_expired" {
		t.Fatalf("malformed session expiry accepted: %+v", response)
	}
}

func TestValidateSessionBindingExpiryAroundDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	actor := identityContext{HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme"}
	binding := func(expires string) *policypkg.Binding {
		return &policypkg.Binding{HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme", ExpiresAt: expires}
	}
	cases := []struct {
		name    string
		expires string
		now     time.Time
		want    error
	}{
		{"before", deadline.Format(time.RFC3339), deadline.Add(-time.Nanosecond), nil},
		{"at", deadline.Format(time.RFC3339), deadline, errSessionExpired},
		{"after", deadline.Format(time.RFC3339), deadline.Add(time.Nanosecond), errSessionExpired},
		{"none", "", deadline.Add(time.Hour), nil},
		{"blank", "  ", deadline.Add(time.Hour), nil},
		{"malformed", "tomorrow", deadline, errSessionExpired},
	}
	for _, tc := range cases {
		if got := validateSessionBinding(binding(tc.expires), actor, tc.now); !errors.Is(got, tc.want) && got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if err := validateSessionBinding(nil, actor, deadline); !errors.Is(err, errSessionUnavailable) {
		t.Fatalf("nil binding: %v", err)
	}
}

func TestAdapterSessionExpiresAtItsDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			t.Parallel()
			s := minimalServer()
			s.now = func() time.Time { return deadline.Add(offset) }
			ex := newTestExchange(http.MethodPost, "/mcp", "", map[string]string{
				defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team/session/session-1",
			})
			ex.R.TLS = verifiedProxyTLS()
			ex.Policy = testPolicy()
			ex.Policy.Auth.TrustDomain = "example.org"
			ex.Policy.Sessions[0].Namespace = "team"
			ex.Policy.Sessions[0].ExpiresAt = deadline.Format(time.RFC3339)
			out, response := s.authFilter(ex.PolicyRequest)
			ex.record(response)
			if offset < 0 {
				if response != nil || out.Identity().SessionID != "session-1" {
					t.Fatal("active adapter session denied")
				}
			} else if response == nil || response.Decision.Reason != "session_expired" || ex.W.status != http.StatusUnauthorized {
				t.Fatalf("expired adapter session: response=%+v status=%d", response, ex.W.status)
			}
			if out.Identity().HumanID != "human-1" {
				t.Fatal("verified actor was lost from expired-session audit state")
			}
		})
	}
}
