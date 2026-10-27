package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/events"
	policypkg "mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/telemetry"
	"mcp-runtime/pkg/transport/httptrace"
)

const upstreamResponseHeaderTimeout = 30 * time.Second

func newUpstreamReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return newUpstreamReverseProxyWithTimeout(target, upstreamResponseHeaderTimeout)
}

func newUpstreamReverseProxyWithTimeout(target *url.URL, responseHeaderTimeout time.Duration) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			req.SetXForwarded()
			sanitizeCredentialRequestTrailers(req)
		},
		Transport:      newUpstreamTransport(responseHeaderTimeout),
		ModifyResponse: sanitizeCredentialResponse,
		ErrorHandler:   handleUpstreamError,
	}
	return proxy
}

func newUpstreamTransport(responseHeaderTimeout time.Duration) http.RoundTripper {
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = upstreamResponseHeaderTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 128
	transport.MaxConnsPerHost = 0 // admission is bounded separately for streams/unary
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return newResilientUpstreamTransport(httptrace.TracedTransport(credentialTrailerTransport{base: transport}), responseHeaderTimeout, 128, 32, 5, 5*time.Second)
}

func handleUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	w = telemetry.CorrelateResponse(w, r.Context())
	logMessage := "gateway upstream error"
	status := http.StatusBadGateway
	errorCode := "upstream_error"
	message := "upstream error"
	var gate upstreamGateError
	if errors.As(err, &gate) {
		status, errorCode, message = gate.status, gate.reason, gate.reason
	}
	if isUpstreamTimeout(err) {
		logMessage = "gateway upstream response timeout"
		status = http.StatusGatewayTimeout
		errorCode = "upstream_timeout"
		message = "upstream response timeout"
	}
	markUpstreamOutcome(r, status, errorCode, gate.reason != "")
	telemetry.RecordSpanFailure(r.Context(), "gateway.upstream", errorCode, status, nil)
	telemetry.LogfCtx(r.Context(), "event=gateway.upstream service=%q message=%q status=%d reason=%q",
		envcfg.EnvOr("OTEL_SERVICE_NAME", "mcp-gateway"), logMessage, status, errorCode)

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errorCode,
		"message": message,
	})
}

func isUpstreamTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// handleGateway is the pipeline orchestrator for the gateway request path.
// It runs the five ordered filters (inspect → policy → auth → authz → upstream)
// and then unconditionally runs stage 6 (audit/analytics finalization).
func (s *gatewayServer) handleGateway(w http.ResponseWriter, r *http.Request) {
	ex := newExchange(telemetry.CorrelateResponse(w, r.Context()), r, s.defaultPolicyVersion)
	// ReverseProxy aborts a partially written response on body read failure.
	// Audit must still run during that unwind, after upstream outcome capture.
	defer func() { s.recordGatewayOutcome(ex); s.emitAuditFromExchange(ex) }()

	policy, _ := s.currentPolicy()
	scope := s.metricScope(policy)
	stopInflight := s.metrics.trackInflight(scope)
	defer stopInflight()
	policyDecisionObserved := false
	defer func() {
		metricScope := s.metricScope(ex.Policy)
		s.metrics.recordRequest(
			metricScope, ex.R, ex.Inspection.Method(), ex.Decision,
			ex.W.status, time.Since(ex.StartTime), ex.R.ContentLength, ex.W.bytes,
		)
		if ex.Inspection.IsToolCall() && ex.Inspection.ToolName() != "" {
			s.metrics.recordToolCall(ex.R.Context(), metricScope, ex.Policy,
				ex.Inspection.ToolName(), ex.Decision, ex.W.status, time.Since(ex.StartTime))
		}
		if policyDecisionObserved || ex.AuthorizationObserved {
			decision := ex.Decision
			if ex.AuthorizationObserved {
				decision = ex.AuthorizationDecision
			}
			s.metrics.recordPolicyDecision(metricScope, ex.Inspection.Method(), decision)
		}
	}()

	s.buildPipeline().Run(ex)
	if ex.Decision.PolicyVersion == "" {
		ex.Decision.PolicyVersion = s.defaultPolicyVersion
	}
	policyDecisionObserved = ex.Inspection.IsToolCall() || ex.Inspection.Indeterminate()
	if !policyDecisionObserved && !ex.Decision.Allowed && !ex.SkipAudit {
		policyDecisionObserved = true
	}
	trace.SpanFromContext(ex.R.Context()).SetAttributes(
		attribute.String("mcp.method.name", metricRPCMethod(ex.Inspection.Method())),
		attribute.Bool("mcp.async", false),
		attribute.String("mcp.server.name", s.metricScope(ex.Policy).Server),
		attribute.String("service.namespace", s.metricScope(ex.Policy).Namespace),
	)
	if len(ex.Inspection.ToolName()) > 0 && len(ex.Inspection.ToolName()) <= 128 {
		trace.SpanFromContext(ex.R.Context()).SetAttributes(attribute.String("gen_ai.tool.name", ex.Inspection.ToolName()))
	}
}

// recordGatewayOutcome marks the request span as failed and writes a
// trace-correlated log line for denied or failed requests. Only the bounded
// decision reason, RPC method, and status are recorded: never tokens, headers,
// tool arguments, or bodies. Upstream failures are recorded by
// handleUpstreamError.
func (s *gatewayServer) recordGatewayOutcome(ex *Exchange) {
	status := ex.W.status
	if !telemetry.IsProbePath(ex.R) || status >= http.StatusBadRequest {
		decision := "allow"
		if !ex.Decision.Allowed && !ex.SkipAudit {
			decision = "deny"
		}
		if ex.SkipAudit {
			decision = "skip"
		}
		scope := s.metricScope(ex.Policy)
		tool := "none"
		if ex.Inspection.IsToolCall() {
			tool = "__other__"
			if ex.Policy != nil && len(ex.Inspection.ToolName()) <= 128 {
				for _, configured := range ex.Policy.Tools {
					if string(configured.Name) == ex.Inspection.ToolName() {
						tool = ex.Inspection.ToolName()
						break
					}
				}
			}
		}
		telemetry.LogfCtx(ex.R.Context(), "event=gateway.request service=%q namespace=%q server=%q tool=%q policy_version=%q method=%q status=%d decision=%q reason=%q duration_ms=%.3f",
			envcfg.EnvOr("OTEL_SERVICE_NAME", "mcp-gateway"), scope.Namespace, scope.Server, tool, ex.Decision.PolicyVersion, metricRPCMethod(ex.Inspection.Method()), status,
			decision, ex.Decision.Reason, float64(time.Since(ex.StartTime))/float64(time.Millisecond))
	}
	denied := !ex.Decision.Allowed && !ex.SkipAudit
	if !denied && status < http.StatusBadRequest {
		return
	}
	if status >= http.StatusInternalServerError && ex.Decision.Allowed {
		return
	}
	reason := ex.Decision.Reason
	if reason == "" {
		reason = http.StatusText(status)
	}
	method := metricRPCMethod(ex.Inspection.Method())
	ctx := ex.R.Context()
	telemetry.RecordSpanFailure(ctx, "gateway.request", reason, status, nil)
	telemetry.LogfCtx(ctx, "gateway request rejected method=%q status=%d reason=%q duration=%s",
		method, status, reason, time.Since(ex.StartTime))
}

// emitAuditFromExchange is stage 6 of the pipeline. It emits a single audit
// event after the pipeline completes, preserving the following semantics from
// the previous monolithic handleGateway:
//
//   - Allowed requests: audit when rpcMethod is non-empty (actual RPC traffic).
//   - Denied requests: audit when rpcMethod is non-empty OR the request was a
//     genuine MCP client attempt (application/json body that failed parsing).
//   - Internal platform service probes (mcp-runtime-live-inventory) are never audited.
func (s *gatewayServer) emitAuditFromExchange(ex *Exchange) {
	if ex.SkipAudit {
		return
	}
	if ex.Identity.AgentID == "mcp-runtime-live-inventory" {
		return
	}
	rpcMethod := ex.Inspection.Method()
	if ex.Decision.Allowed {
		if rpcMethod == "" {
			return
		}
	} else if rpcMethod == "" && !ex.Inspection.IsRPCAttempt() {
		return
	}
	envelope, err := events.NewEnvelope(s.source, s.eventType, s.auditPayload(ex, time.Since(ex.StartTime)), s.currentTime().UTC())
	if err == nil {
		s.emitIfEnabled(ex.R.Context(), envelope)
	}
}

// deniedFilterResponse writes a denial and returns its terminal decision.
// Audit emission is intentionally absent here; it is centralised in
// emitAuditFromExchange (stage 6) so each denial is audited exactly once.
func writeDeniedResponse(w http.ResponseWriter, policy *policypkg.Document, toolName string, decision policypkg.Decision) policypkg.Decision {
	w.Header().Set("content-type", "application/json")
	if shouldChallengeOAuth(policy, decision) {
		w.Header().Set("www-authenticate", oauthAuthenticateHeader(
			policy, decision.Reason, toolName, decision,
		))
	}
	status := gatewayDeniedStatus(policy, decision)
	decision.Status = status
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gatewayDeniedPayload(policy, decision))
	return decision
}

func gatewayDeniedStatus(policy *policypkg.Document, decision policypkg.Decision) int {
	if decision.Status > 0 {
		return decision.Status
	}
	return http.StatusForbidden
}

func gatewayDeniedPayload(_ *policypkg.Document, decision policypkg.Decision) map[string]any {
	return map[string]any{"error": decision.Reason}
}

// auditPayload maps the typed observation record to the shared wire contract.
func (s *gatewayServer) auditPayload(ex *Exchange, elapsed time.Duration) events.MCPRequest {
	policy, decision, actor := ex.Policy, ex.Decision, ex.Identity
	payload := events.MCPRequest{
		Method: ex.R.Method, Path: ex.OriginalPath, Status: ex.W.status,
		LatencyMS: elapsed.Milliseconds(), BytesIn: maxInt64(ex.R.ContentLength, 0), BytesOut: ex.W.bytes,
		Server:    policypkg.FirstNonEmpty(policypkg.PolicyServerName(policy), s.serverName),
		Namespace: policypkg.FirstNonEmpty(policypkg.PolicyServerNamespace(policy), s.serverNamespace),
		TeamID:    policypkg.PolicyServerTeamID(policy), ResourceTeamID: policypkg.PolicyServerTeamID(policy),
		Cluster: policypkg.FirstNonEmpty(policypkg.PolicyServerCluster(policy), s.clusterName),
		HumanID: string(actor.HumanID), AgentID: string(actor.AgentID), SubjectTeamID: string(actor.TeamID), SessionID: string(actor.SessionID),
		Decision: ternary(decision.Allowed, "allow", "deny"), Reason: decision.Reason,
		PolicyVersion: policypkg.FirstNonEmpty(decision.PolicyVersion, s.defaultPolicyVersion),
		ServerUID:     s.serverUID, WouldDeny: decision.WouldDeny, WouldDenyReason: ternary(decision.WouldDeny, decision.WouldDenyReason, ""),
		Idempotency: ex.Idempotency.Outcome, IdempotencyKeyHash: ex.Idempotency.KeyHash,
		RPCMethod: ex.Inspection.Method(), ToolName: ex.Inspection.ToolName(),
		MatchedGrant: decision.MatchedGrant, MatchedSession: decision.MatchedSession,
		RequiredTrust: decision.RequiredTrust, RequiredSideEffect: decision.RequiredSideEffect,
		RiskLevel:  policypkg.FirstNonEmpty(decision.RiskLevel, policypkg.ToolRiskLevel(policy, ex.Inspection.ToolName())),
		AdminTrust: decision.AdminTrust, ConsentedTrust: decision.ConsentedTrust, EffectiveTrust: decision.EffectiveTrust,
	}
	if decision.MatchedGrant != "" {
		payload.MatchedGrantNamespace = decision.MatchedGrantNamespace
	}
	if decision.MatchedSession != "" {
		payload.MatchedSessionNamespace = decision.MatchedSessionNamespace
	}
	if policypkg.UsesDelegatedHeaders(policy) {
		payload.AuthDelegation, payload.CallerIdentity = "upstream_header", "unverified"
	}
	return payload
}

func absoluteRequestURL(r *http.Request, requestPath string) string {
	path := normalizeURLPath(requestPath)
	if r == nil {
		return path
	}

	host := ""
	if strings.TrimSpace(r.Host) != "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" && r.URL != nil && strings.TrimSpace(r.URL.Host) != "" {
		host = strings.TrimSpace(r.URL.Host)
	}
	if host == "" {
		return path
	}

	scheme := "http"
	if r.URL != nil && r.URL.Scheme != "" {
		scheme = r.URL.Scheme
	} else if r.TLS != nil {
		scheme = "https"
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   path,
	}).String()
}

func parseExternalBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("must be an absolute URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func resolveBaseURLPath(base *url.URL, requestPath string) string {
	if base == nil {
		return normalizeURLPath(requestPath)
	}
	resolved := *base
	resolved.Path = path.Join(strings.TrimRight(base.Path, "/"), normalizeURLPath(requestPath))
	if !strings.HasPrefix(resolved.Path, "/") {
		resolved.Path = "/" + resolved.Path
	}
	resolved.RawPath = ""
	return resolved.String()
}

func normalizeURLPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

func ternary(condition bool, truthy, falsy string) string {
	if condition {
		return truthy
	}
	return falsy
}

func trimRequestPathPrefix(value, prefix string) (string, bool) {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return value, false
	}
	if value != prefix && !strings.HasPrefix(value, prefix+"/") {
		return value, false
	}
	return strings.TrimPrefix(value, prefix), true
}
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write records response data and updates byte count.
func (r *statusRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

// Flush forwards flush calls to the underlying ResponseWriter.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards hijack calls to the underlying ResponseWriter.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacker not supported")
	}
	return hijacker.Hijack()
}

// Push forwards HTTP/2 server push calls to the underlying ResponseWriter.
func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func maxInt64(value, fallback int64) int64 {
	if value < 0 {
		return fallback
	}
	return value
}
