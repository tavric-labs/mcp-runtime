package main

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	policypkg "mcp-runtime/pkg/policy"
)

// Filter transforms one pipeline phase into the next. A nil response continues
// processing. A non-nil response describes the terminal HTTP response;
// its output still carries partial state needed for audit and metrics.
type Filter[In, Out any] interface {
	Handle(In) (Out, *FilterResponse)
}

// FilterFunc lets production stages and test doubles implement the same contract.
type FilterFunc[In, Out any] func(In) (Out, *FilterResponse)

func (f FilterFunc[In, Out]) Handle(in In) (Out, *FilterResponse) { return f(in) }

// FilterResponse terminates the pipeline with an explicit decision. Only public
// OAuth metadata skips audit; failed authentication retains its partial identity.
type FilterResponse struct {
	Decision policypkg.Decision
	kind     responseKind
	metadata *oauthMetadataResponse
}

type responseKind uint8

const (
	responseDenied responseKind = iota + 1
	responseMetadata
	responseForwarded
)

func denyResponse(decision policypkg.Decision) *FilterResponse {
	return &FilterResponse{Decision: decision, kind: responseDenied}
}

func forwardedResponse(decision policypkg.Decision) *FilterResponse {
	return &FilterResponse{Decision: decision, kind: responseForwarded}
}

// RequestInput owns the HTTP request, shared by all phases. Policy
// and identity travel separately as values; filters must treat referenced policy
// documents and session bindings as immutable snapshots.
type RequestInput struct {
	R *http.Request
}

type InspectedRequest struct {
	*RequestInput
	Inspection rpcInspection
}

type PolicyRequest struct {
	InspectedRequest
	Policy    *policypkg.Document
	PolicyErr error
}

type AuthenticatedRequest struct {
	PolicyRequest
	Identity   identityContext
	OAuthToken string
	CallerKind CallerKind
}

// Authentication completed does not imply a Runtime identity: delegated-header
// and anonymous/observe traffic are explicit, supported caller categories.
type CallerKind uint8

const (
	CallerAnonymous CallerKind = iota
	CallerDelegated
	CallerOAuth
	CallerCertificate
)

type AuthorizedRequest struct {
	AuthenticatedRequest
	Decision policypkg.Decision
}

// PermittedRequest is the upstream capability. Only the dispatch gate attaches
// the HTTP writer, after checking authorization. Its zero value cannot dispatch.
type PermittedRequest struct {
	request AuthorizedRequest
	writer  *statusRecorder
	valid   bool
	outcome *dispatchOutcome
}

func permitRequest(request AuthorizedRequest, writer *statusRecorder) (PermittedRequest, *FilterResponse) {
	if !request.Decision.Allowed {
		decision := request.Decision
		if decision.Reason == "" {
			decision = policypkg.Deny(http.StatusForbidden, "undecided", policypkg.PolicyVersion(request.Policy))
		}
		return PermittedRequest{}, denyResponse(decision)
	}
	if request.RequestInput == nil || request.R == nil || writer == nil {
		return PermittedRequest{}, denyResponse(policypkg.Deny(http.StatusInternalServerError, "invalid_dispatch", request.Decision.PolicyVersion))
	}
	return PermittedRequest{request: request, writer: writer, valid: true, outcome: &dispatchOutcome{Decision: request.Decision}}, nil
}

// Pipeline has a fixed security-sensitive order. Named, typed slots allow a
// boundary to be replaced in tests without permitting arbitrary stage ordering.
// Exchange is an observation record, never a filter's input.
type Pipeline struct {
	Inspect  Filter[*RequestInput, InspectedRequest]
	Policy   Filter[InspectedRequest, PolicyRequest]
	Auth     Filter[PolicyRequest, AuthenticatedRequest]
	Authz    Filter[AuthenticatedRequest, AuthorizedRequest]
	Upstream Filter[PermittedRequest, AuthorizedRequest]
}

func (s *gatewayServer) buildPipeline() Pipeline {
	return Pipeline{
		Inspect:  FilterFunc[*RequestInput, InspectedRequest](s.inspectFilter),
		Policy:   FilterFunc[InspectedRequest, PolicyRequest](s.policyFilter),
		Auth:     FilterFunc[PolicyRequest, AuthenticatedRequest](s.authFilter),
		Authz:    FilterFunc[AuthenticatedRequest, AuthorizedRequest](s.authzFilter),
		Upstream: FilterFunc[PermittedRequest, AuthorizedRequest](s.upstreamFilter),
	}
}

// Run records each completed phase, including terminal paths, so the HTTP
// orchestrator can finalize audit and metrics exactly once after it returns.
func (p Pipeline) Run(ex *Exchange) {
	request := &RequestInput{R: ex.R}
	var inspected InspectedRequest
	if runStage(ex, request, "inspect", func() *FilterResponse {
		var response *FilterResponse
		inspected, response = p.Inspect.Handle(request)
		ex.Inspection = inspected.Inspection
		return response
	}) {
		return
	}
	var policy PolicyRequest
	if runStage(ex, request, "policy", func() *FilterResponse {
		var response *FilterResponse
		policy, response = p.Policy.Handle(inspected)
		ex.Policy, ex.PolicyErr = policy.Policy, policy.PolicyErr
		return response
	}) {
		return
	}
	var authenticated AuthenticatedRequest
	if runStage(ex, request, "auth", func() *FilterResponse {
		var response *FilterResponse
		authenticated, response = p.Auth.Handle(policy)
		ex.Identity, ex.OAuthToken = authenticated.Identity, authenticated.OAuthToken
		return response
	}) {
		return
	}
	var authorized AuthorizedRequest
	if runStage(ex, request, "authz", func() *FilterResponse {
		var response *FilterResponse
		authorized, response = p.Authz.Handle(authenticated)
		ex.Decision = authorized.Decision
		ex.AuthorizationDecision = authorized.Decision
		ex.AuthorizationObserved = inspected.Inspection.RequiresAuthorization() || !authorized.Decision.Allowed
		return response
	}) {
		return
	}
	runStage(ex, request, "upstream", func() *FilterResponse {
		permitted, response := permitRequest(authorized, ex.W)
		if response != nil {
			return response
		}
		defer func() {
			ex.Decision, ex.Idempotency = permitted.outcome.Decision, permitted.outcome.Idempotency
		}()
		_, response = p.Upstream.Handle(permitted)
		return response
	})
}

// runStage keeps tracing coupled to the stage it actually executes and restores
// the parent context even when the stage panics.
func runStage(ex *Exchange, request *RequestInput, name string, handle func() *FilterResponse) bool {
	parent := request.R.Context()
	ctx, span := otel.Tracer("mcp-runtime/gateway").Start(parent,
		"gateway."+name, trace.WithSpanKind(trace.SpanKindInternal))
	request.R = request.R.WithContext(ctx)
	completed := false
	var response *FilterResponse
	defer func() {
		span.SetAttributes(attribute.String("mcp.method.name", metricRPCMethod(ex.Inspection.Method())),
			attribute.String("gateway.stage", name),
			attribute.String("gateway.decision.reason", ex.Decision.Reason))
		if !completed || ex.W.status >= http.StatusBadRequest || response != nil && ex.Decision.Status >= http.StatusBadRequest {
			span.SetStatus(codes.Error, "gateway request failed")
		}
		request.R = request.R.WithContext(parent)
		ex.R = request.R
		span.End()
	}()
	response = handle()
	if response != nil {
		applyFilterResponse(ex, response)
	}
	completed = true
	return response != nil
}
