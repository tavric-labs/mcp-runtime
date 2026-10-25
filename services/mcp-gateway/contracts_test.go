package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

type sessionReaderStub struct {
	lookup func(context.Context, SessionRef, time.Time) (*policypkg.Binding, error)
	fresh  bool
}

func (s sessionReaderStub) Lookup(ctx context.Context, ref SessionRef, now time.Time) (*policypkg.Binding, error) {
	return s.lookup(ctx, ref, now)
}
func (s sessionReaderStub) Fresh(time.Time) bool { return s.fresh }

var _ SessionReader = (*gatewaySessionSync)(nil)
var _ OAuthAuthenticator = oauthAuthenticatorFunc(nil)

func TestCertificateAuthenticationUsesTheScopedSessionReader(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	s := minimalServer()
	s.now = func() time.Time { return now }
	calls := 0
	s.sessions = sessionReaderStub{fresh: true, lookup: func(_ context.Context, ref SessionRef, at time.Time) (*policypkg.Binding, error) {
		calls++
		if ref != (SessionRef{Namespace: "team", ID: "session-1"}) || at != now {
			t.Fatalf("lookup ref=%+v at=%s", ref, at)
		}
		return &policypkg.Binding{Namespace: ref.Namespace, Name: ref.ID, HumanID: "human", AgentID: "agent", TeamID: "team", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}, nil
	}}
	ex := newTestExchange(http.MethodPost, "/mcp", "", map[string]string{defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team/session/session-1"})
	ex.Policy = testPolicy()
	ex.Policy.Auth.TrustDomain = "example.org"
	ex.Policy.Session.Store = "platform"
	ex.R.TLS = verifiedProxyTLS()
	out, response := s.authFilter(ex.PolicyRequest)
	if response != nil || calls != 1 || out.CallerKind != CallerCertificate || out.Identity.AgentID != "agent" {
		t.Fatalf("authentication response=%+v calls=%d caller=%v identity=%+v", response, calls, out.CallerKind, out.Identity)
	}
}

func TestAuthenticationClassifiesWrappedFailuresWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err    error
		status int
		reason string
	}{
		{fmt.Errorf("store: %w", errSessionUnavailable), http.StatusServiceUnavailable, "policy_unavailable"},
		{fmt.Errorf("store: %w", errSessionRevoked), http.StatusUnauthorized, "session_revoked"},
		{fmt.Errorf("verify: %w", &authenticationFailure{status: http.StatusUnauthorized, reason: "invalid_token"}), http.StatusUnauthorized, "invalid_token"},
		{errors.New("private-provider-secret"), http.StatusServiceUnavailable, "authentication_unavailable"},
	} {
		got := authenticationDecision(tc.err, "policy-version")
		if got.Allowed || got.Status != tc.status || got.Reason != tc.reason || got.PolicyVersion != "policy-version" {
			t.Fatalf("decision=%+v", got)
		}
	}
}

func TestOAuthAuthenticationCanBeExercisedWithoutDiscoveryOrJWKS(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	s.oauth = oauthAuthenticatorFunc(func(_ *http.Request, _ *policypkg.Document, team policypkg.TeamID) (oauthCredentials, error) {
		if team != "" {
			t.Fatal("direct request unexpectedly acquired a certificate team")
		}
		return oauthCredentials{Identity: identityContext{HumanID: "human", AgentID: "agent"}, Token: "verified-token"}, nil
	})
	ex := newTestExchange(http.MethodPost, "/mcp", "", nil)
	ex.Policy = oauthPolicy("https://issuer.invalid")
	out, response := s.authFilter(ex.PolicyRequest)
	if response != nil || out.CallerKind != CallerOAuth || out.Identity.HumanID != "human" || out.OAuthToken != "verified-token" {
		t.Fatalf("out=%+v response=%+v", out.Identity, response)
	}
}

func TestInvalidTerminalOutcomesFailClosed(t *testing.T) {
	t.Parallel()
	for _, response := range []*FilterResponse{
		{},
		denyResponse(policypkg.Allow("contradiction", "test")),
		denyResponse(policypkg.Deny(http.StatusOK, "contradiction", "test")),
		{kind: responseMetadata},
		{kind: responseMetadata, metadata: &oauthMetadataResponse{}},
	} {
		w := httptest.NewRecorder()
		ex := newExchange(w, httptest.NewRequest(http.MethodGet, "/mcp", nil), "test")
		applyFilterResponse(ex, response)
		if ex.SkipAudit || ex.Decision.Allowed || w.Code != http.StatusInternalServerError {
			t.Fatalf("invalid outcome accepted: status=%d decision=%+v", w.Code, ex.Decision)
		}
	}
}

func TestZeroDispatchCapabilityCannotReachUpstream(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	_, response := s.upstreamFilter(PermittedRequest{})
	if response == nil || response.Decision.Allowed {
		t.Fatal("zero dispatch capability accepted")
	}
}

// A malformed expiry has always been treated as "no expiry" (mtlsBindingExpired
// ignored parse errors). Changing that is a behavior change that needs review.
func TestMalformedSessionDeadlineKeepsLegacyNoExpiryBehavior(t *testing.T) {
	t.Parallel()
	actor := identityContext{HumanID: "human"}
	err := validateSessionBinding(&policypkg.Binding{HumanID: actor.HumanID, ExpiresAt: "tomorrow"}, actor, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("malformed expiry changed outcome: %v", err)
	}
}

func TestRPCMethodsKeepTheirAuditIdentityAcrossProtocolRevisions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, method := range []string{"tools/list", "resources/read", "prompts/get", "example.test/custom"} {
			t.Run(version+"/"+method, func(t *testing.T) {
				t.Parallel()
				s := minimalServer()
				policy := testPolicy()
				policy.Policy.Mode = "observe"
				s.snapshotPolicy(policySnapshot{Policy: policy, Ready: true})
				s.proxy = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
				body := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":%q,\"params\":{\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":%q},\"name\":\"not-a-tool\",\"private\":\"do-not-audit\"}}", method, version)
				r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
				r.Header.Set("Mcp-Protocol-Version", version)
				ex := newExchange(httptest.NewRecorder(), r, "test")
				s.buildPipeline().Run(ex)
				payload := s.auditPayload(ex, time.Millisecond)
				if payload.RPCMethod != method || payload.ToolName != "not-a-tool" || !ex.Decision.Allowed {
					t.Fatalf("observation=%+v decision=%+v", payload, ex.Decision)
				}
				raw, err := json.Marshal(payload)
				if err != nil || strings.Contains(string(raw), "do-not-audit") || strings.Contains(string(raw), "_meta") {
					t.Fatal("audit retained raw method params")
				}
			})
		}
	}
}
