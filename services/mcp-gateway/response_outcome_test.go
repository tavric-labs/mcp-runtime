package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// Public OAuth metadata is the only outcome that skips audit. It is rendered
// by the single response owner and never evaluates identity or authorization.
func TestMetadataOutcomeIsRenderedAndExcludedFromAudit(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	issuer := newTestJWTIssuer(t)
	s.snapshotPolicy(policySnapshot{Policy: oauthPolicy(issuer.url), Ready: true})
	w := httptest.NewRecorder()
	ex := newExchange(w, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil), "test")
	s.buildPipeline().Run(ex)
	if w.Code != http.StatusOK || w.Header().Get("content-type") != "application/json" {
		t.Fatalf("status=%d type=%q", w.Code, w.Header().Get("content-type"))
	}
	if !ex.SkipAudit || !ex.Decision.Allowed || ex.Decision.Reason != "oauth_metadata" {
		t.Fatalf("SkipAudit=%v decision=%+v", ex.SkipAudit, ex.Decision)
	}
	if ex.AuthorizationObserved || ex.Identity != (identityContext{}) || ex.OAuthToken != "" {
		t.Fatalf("metadata path established identity or authorization: %+v", ex)
	}
}

// A denial from any stage keeps its partial state for audit: the verified
// actor from authentication, and the evaluated decision from authorization.
func TestDeniedOutcomesKeepPartialStateForAudit(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	s.now = func() time.Time { return time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC) }
	policy := testPolicy()
	policy.Auth.TrustDomain = "example.org"
	policy.Sessions[0].Namespace = "team"
	policy.Sessions[0].Revoked = true
	s.snapshotPolicy(policySnapshot{Policy: policy, Ready: true})
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	r.Header.Set(defaultVerifiedSPIFFEHeader, "spiffe://example.org/ns/team/session/session-1")
	r.TLS = verifiedProxyTLS()
	w := httptest.NewRecorder()
	ex := newExchange(w, r, "test")
	s.buildPipeline().Run(ex)
	if w.Code != http.StatusUnauthorized || ex.Decision.Allowed || ex.Decision.Reason != "session_revoked" {
		t.Fatalf("status=%d decision=%+v", w.Code, ex.Decision)
	}
	if ex.SkipAudit || ex.Identity.HumanID != "human-1" || ex.Identity.SessionID != "session-1" {
		t.Fatalf("denial lost its audit attribution: skip=%v actor=%+v", ex.SkipAudit, ex.Identity)
	}
	payload := s.auditPayload(ex, time.Millisecond)
	if payload.Decision != "deny" || payload.HumanID != "human-1" {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestEveryOutcomeKindFailsClosedWhenMalformed(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]*FilterResponse{
		"zero value":                    {},
		"unknown kind":                  {kind: responseKind(200)},
		"allowed denial":                denyResponse(policypkg.Allow("x", "v")),
		"success status denial":         denyResponse(policypkg.Deny(http.StatusOK, "x", "v")),
		"metadata without a body":       metadataResponse(policypkg.Allow("oauth_metadata", "v"), nil),
		"metadata with a zero status":   metadataResponse(policypkg.Allow("oauth_metadata", "v"), &oauthMetadataResponse{}),
		"metadata with an error status": metadataResponse(policypkg.Allow("oauth_metadata", "v"), &oauthMetadataResponse{status: 600}),
	} {
		w := httptest.NewRecorder()
		ex := newExchange(w, httptest.NewRequest(http.MethodGet, "/mcp", nil), "test")
		applyFilterResponse(ex, response)
		if w.Code != http.StatusInternalServerError || ex.Decision.Allowed || ex.SkipAudit || ex.Decision.Reason != "invalid_filter_response" {
			t.Errorf("%s: status=%d decision=%+v skip=%v", name, w.Code, ex.Decision, ex.SkipAudit)
		}
	}
}
