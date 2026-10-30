package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	policypkg "mcp-runtime/pkg/policy"
)

func TestReleaseProtocolTransportMatrix(t *testing.T) {
	t.Parallel()
	issuer := newTestJWTIssuer(t)
	for _, version := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		for _, format := range []string{"application/json", "text/event-stream"} {
			for _, auth := range []string{"certificate", "oauth", "certificate+oauth"} {
				for _, outcome := range []string{"allowed", "ungranted", "revoked", "expired"} {
					t.Run(version+"/"+format+"/"+auth+"/"+outcome, func(t *testing.T) {
						t.Parallel()
						policy := oauthPolicy(issuer.url)
						policy.Server.Namespace = "team-acme"
						policy.Session.Store = "platform"
						if auth == "certificate" {
							policy.Auth.IssuerURL = ""
							policy.Auth.Audience = ""
						}
						if auth != "oauth" {
							policy.Auth.TrustDomain = "example.org"
						}
						if outcome == "ungranted" {
							policy.Grants = nil
						}
						binding := &policypkg.Binding{Namespace: "team-acme", Name: "session-1", HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme", ConsentedTrust: "high", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
						if outcome == "revoked" {
							binding.Revoked = true
						}
						if outcome == "expired" {
							binding.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
						}
						var calls atomic.Int32
						body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","_meta":{"io.modelcontextprotocol/protocolVersion":%q}}}`, version)
						server := newTestGatewayServer(t, policy, func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							got, err := io.ReadAll(r.Body)
							if err != nil || string(got) != body || r.Header.Get("Mcp-Protocol-Version") != version {
								t.Errorf("gateway changed protocol request: headers=%v body=%s err=%v", r.Header, got, err)
							}
							w.Header().Set("Content-Type", format)
							if format == "text/event-stream" {
								fmt.Fprint(w, "data: ")
							}
							fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
							if format == "text/event-stream" {
								fmt.Fprint(w, "\n\n")
								w.(http.Flusher).Flush()
							}
						})
						server.sessions = sessionReaderStub{fresh: true, lookup: func(_ context.Context, ref SessionRef, _ time.Time) (*policypkg.Binding, error) {
							if ref != (SessionRef{Namespace: "team-acme", ID: "session-1"}) {
								t.Errorf("wrong session scope: %+v", ref)
							}
							return binding, nil
						}}
						req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json, text/event-stream")
						req.Header.Set("Mcp-Protocol-Version", version)
						if auth != "certificate" {
							token := issuer.sign(t, jwt.MapClaims{"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human-1", "azp": "client-1", "team_id": "team-acme", "sid": "session-1", "exp": time.Now().Add(time.Hour).Unix()})
							req.Header.Set("Authorization", "Bearer "+token)
						}
						if auth != "oauth" {
							req.Header.Set(defaultVerifiedSPIFFEHeader, "spiffe://example.org/ns/team-acme/session/session-1")
							// TLS verification itself is exercised by live Kind and staging.
							req.TLS = verifiedProxyTLS()
						}
						rec := httptest.NewRecorder()
						server.handleGateway(rec, req)
						want := http.StatusOK
						if outcome == "ungranted" {
							want = http.StatusForbidden
						} else if outcome != "allowed" {
							want = http.StatusUnauthorized
						}
						if rec.Code != want {
							t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
						}
						if outcome == "allowed" {
							if calls.Load() != 1 || rec.Header().Get("Content-Type") != format || !strings.Contains(rec.Body.String(), `"content":[]`) {
								t.Fatalf("upstream response changed: calls=%d headers=%v body=%s", calls.Load(), rec.Header(), rec.Body.String())
							}
						} else if calls.Load() != 0 {
							t.Fatal("denied request reached upstream")
						}
					})
				}
			}
		}
	}
}
