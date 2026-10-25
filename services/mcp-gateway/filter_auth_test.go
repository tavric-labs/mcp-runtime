package main

import (
	"net/http"
	"testing"
	"time"
)

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
				if response != nil || out.Identity.SessionID != "session-1" {
					t.Fatal("active adapter session denied")
				}
			} else if response == nil || response.Decision.Reason != "session_expired" || ex.W.status != http.StatusUnauthorized {
				t.Fatalf("expired adapter session: response=%+v status=%d", response, ex.W.status)
			}
			if out.Identity.HumanID != "human-1" {
				t.Fatal("verified actor was lost from expired-session audit state")
			}
		})
	}
}
