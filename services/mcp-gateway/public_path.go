package main

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// Public paths let a server accept a request that carries no OAuth bearer, for
// features such as single-use signed upload URLs where the upstream
// authenticates the caller by a secret inside the path. The gateway stays a
// policy layer: it does not know what the path is for. It only guarantees the
// route is narrow (declared prefix, POST only), bounded (body size and time),
// stripped of credentials and identity headers, and audited without the path's
// secret. Everything else, including the MCP endpoint, keeps the full pipeline.

const (
	publicPathAllowReason = "public_path"
	// publicPathAuditSuffix replaces the secret part of the path in audit records.
	publicPathAuditSuffix = "{redacted}"
)

// newPublicUploadTransport is a plain transport. Ordinary tool calls go through
// the resilient transport, whose 30s unary timer cancels the request context and
// would kill a long upload mid-body. Public paths are bounded by their own
// per-rule deadline instead, so there is no response-header timeout here.
func newPublicUploadTransport() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 0
	return transport
}

// newPublicUploadProxy forwards public-path requests to the upstream server.
func newPublicUploadProxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			req.SetXForwarded()
		},
		Transport:    newPublicUploadTransport(),
		ErrorHandler: handleUpstreamError,
	}
}

// publicPathMatch resolves a request to a declared public path. The match is on
// the path the upstream sees, after the gateway's own prefix is stripped. A
// request whose path needed percent-decoding to look clean never matches.
func (s *gatewayServer) publicPathMatch(policy *policypkg.Document, r *http.Request) (policypkg.PublicPath, string, bool) {
	if s.uploadProxy == nil || policy == nil || policy.Auth == nil || len(policy.Auth.PublicPaths) == 0 || r.URL.RawPath != "" {
		return policypkg.PublicPath{}, "", false
	}
	upstreamPath := r.URL.Path
	if trimmed, ok := trimRequestPathPrefix(upstreamPath, s.stripPrefix); ok {
		upstreamPath = trimmed
	}
	rule, ok := policypkg.MatchPublicPath(policy, r.Method, upstreamPath)
	return rule, upstreamPath, ok
}

// servePublicPath handles one matched request. It never authenticates or
// authorizes: the declared rule is the whole contract.
func (s *gatewayServer) servePublicPath(ex *Exchange, policy *policypkg.Document, rule policypkg.PublicPath, upstreamPath string) {
	version := policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(policy), s.defaultPolicyVersion)
	ex.Policy = policy
	ex.PublicPath = true
	// The path carries the caller's secret; audit and metrics get the prefix only.
	ex.OriginalPath = rule.PathPrefix + publicPathAuditSuffix

	reject := func(status int, reason string) {
		ex.Decision = writeDeniedResponse(ex.W, policy, "", policypkg.Deny(status, reason, version))
	}
	switch {
	case ex.R.ContentLength < 0:
		reject(http.StatusLengthRequired, "length_required")
		return
	case ex.R.ContentLength > rule.MaxBodyBytes:
		reject(http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}

	limit := time.Duration(rule.MaxDurationSeconds) * time.Second
	control := http.NewResponseController(ex.W)
	_ = control.SetReadDeadline(time.Now().Add(limit))
	_ = control.SetWriteDeadline(time.Now().Add(limit))
	ctx, cancel := context.WithTimeout(ex.R.Context(), limit)
	defer cancel()
	outcome := &upstreamOutcome{}
	ctx = context.WithValue(ctx, upstreamOutcomeKey{}, outcome)
	r := ex.R.WithContext(ctx)
	ex.R = r

	// No credential or identity assertion may reach a server through a route
	// that skipped authentication: the bearer is useless there, and the
	// verified-identity header could be forged.
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", s.verifiedSPIFFEHeaderName(), oauthTokenHeader(policy)} {
		r.Header.Del(name)
	}
	r.Body = http.MaxBytesReader(ex.W, r.Body, rule.MaxBodyBytes)
	r.URL.Path, r.URL.RawPath = upstreamPath, ""

	ex.Decision = policypkg.Allow(publicPathAllowReason, version)
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
	s.uploadProxy.ServeHTTP(ex.W, r)
}
