package main

import (
	"context"
	"net/http"

	policypkg "mcp-runtime/pkg/policy"
)

// upstreamContext holds dispatch inputs and operational outcomes. Policy and
// session snapshots remain read-only; pre-dispatch filters never receive it.
type upstreamContext struct {
	R           *http.Request
	W           *statusRecorder
	Inspection  rpcInspection
	Policy      *policypkg.Document
	Identity    identityContext
	Decision    policypkg.Decision
	Idempotency idempotencyAudit
}

// dispatchOutcome carries operational results back to observation even when a
// streaming proxy aborts the response by panicking.
type dispatchOutcome struct {
	decision    policypkg.Decision
	idempotency idempotencyAudit
}

// upstreamFilter dispatches only a capability issued by the authorization gate.
func (s *gatewayServer) upstreamFilter(in PermittedRequest) (AuthorizedRequest, *FilterResponse) {
	request := in.request
	if !in.valid || in.writer == nil || in.outcome == nil || !request.Decision.Allowed {
		return request, denyResponse(policypkg.Deny(http.StatusForbidden, "undecided", request.Decision.PolicyVersion))
	}
	ex := &upstreamContext{R: request.R, W: in.writer, Inspection: request.Inspection, Policy: request.Policy, Identity: request.Identity, Decision: request.Decision}
	defer func() {
		in.outcome.decision, in.outcome.idempotency = ex.Decision, ex.Idempotency
		request.R = ex.R
	}()
	if len(ex.R.Header.Values(idempotencyKeyHeader)) > 0 {
		s.idempotentUpstream(ex)
	} else {
		s.forwardUpstream(ex)
	}
	request.Decision = ex.Decision
	return request, forwardedResponse(ex.Decision)
}

// forwardUpstream prepares and proxies the request to the upstream MCP server.
func (s *gatewayServer) forwardUpstream(ex *upstreamContext) {
	if !s.injectCredentials(ex) {
		ex.Decision = policypkg.Deny(http.StatusServiceUnavailable, "credential_unavailable", ex.Decision.PolicyVersion)
		http.Error(ex.W, "credential unavailable", http.StatusServiceUnavailable)
		return
	}
	outcome := &upstreamOutcome{}
	ex.R = ex.R.WithContext(context.WithValue(ex.R.Context(), upstreamOutcomeKey{}, outcome))
	defer func() {
		outcome.mu.Lock()
		defer outcome.mu.Unlock()
		if outcome.reason != "" {
			ex.Decision.Reason, ex.Decision.Status = outcome.reason, outcome.status
			if outcome.reject {
				ex.Decision.Allowed = false
			}
		}
	}()
	// The verified SPIFFE header is an ingress-to-gateway assertion; the MCP
	// server must never see it, forged or not.
	ex.R.Header.Del(s.verifiedSPIFFEHeaderName())
	// Header mode forwards every name in auth.headers unchanged. There is no
	// scheme or vendor list. Authorization is removed only when this server
	// did not configure it and the request is not an OAuth-validated token.
	if !policypkg.PolicyUsesOAuth(ex.Policy) && !policypkg.DelegatedHeaderConfigured(ex.Policy, defaultTokenHeader) {
		ex.R.Header.Del(defaultTokenHeader)
	}
	if trimmedPath, ok := trimRequestPathPrefix(ex.R.URL.Path, s.stripPrefix); ok {
		ex.R.URL.Path = trimmedPath
		// Always clear RawPath when Path was trimmed. If RawPath trims cleanly
		// keep the percent-encoded form; otherwise clear it so Go's URL machinery
		// falls back to escaping Path — preventing an inconsistent URL where Path
		// is stripped but RawPath still carries the prefix.
		if trimmedRaw, rawOK := trimRequestPathPrefix(ex.R.URL.RawPath, s.stripPrefix); rawOK {
			ex.R.URL.RawPath = trimmedRaw
		} else {
			ex.R.URL.RawPath = ""
		}
		if ex.R.URL.Path == "" {
			ex.R.URL.Path = "/"
			if ex.R.URL.RawPath != "" {
				ex.R.URL.RawPath = "/"
			}
		}
	}

	s.proxy.ServeHTTP(ex.W, ex.R)
}
