// Package main implements the MCP gateway service.
//
// # Request filter pipeline
//
// Every inbound request is processed through a fixed ordered pipeline of five
// stages before audit/analytics finalization runs unconditionally as stage 6.
// Stage order is security-sensitive and is fixed in code:
//
//	Stage 1 – InspectFilter:   bounded body capture; RPC method and tool name extraction
//	Stage 2 – PolicyFilter:    atomic policy snapshot acquisition; OAuth metadata early-exit
//	Stage 3 – AuthFilter:      optional OAuth and adapter-certificate authentication
//	Stage 4 – AuthzFilter:     authorization and session/grant evaluation
//	Stage 5 – UpstreamFilter:  credential cleanup; path rewrite; upstream proxy
//	Stage 6 – (orchestrator):  audit/analytics finalization
//
// Ordering guarantees:
//   - Authentication (stage 3) always completes before authorization (stage 4).
//   - The policy snapshot (stage 2) is captured once and held immutable for the
//     entire exchange lifetime; no later stage may re-acquire it.
//   - Authorization consumes AuthenticatedRequest and produces AuthorizedRequest;
//     upstream preparation (stage 5) only reads its policy and identity.
//
// Filters have typed phase inputs and outputs (see filter.go). A non-nil
// FilterResponse halts processing; stage 6 runs from the HTTP orchestrator.
package main

import (
	"net/http"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// Exchange records request-scoped state for audit, metrics and tracing.
// Pipeline copies completed phase outputs here; filters receive typed inputs
// instead of this mutable observation record.
type Exchange struct {
	// W wraps the original ResponseWriter to capture status code and byte count.
	W *statusRecorder
	// R is the latest HTTP request, with its parent trace context restored after
	// each stage. Inspection may buffer Body; upstream may rewrite its path.
	R *http.Request
	// OriginalPath is r.URL.Path captured before any prefix stripping.
	OriginalPath string
	// StartTime is captured by the orchestrator for latency accounting.
	StartTime time.Time

	// Set by stage 1 (InspectFilter).
	Inspection rpcInspection

	// Set by stage 2 (PolicyFilter). Immutable for the lifetime of this exchange.
	Policy    *policypkg.Document
	PolicyErr error

	// Set by stage 3 (AuthFilter). Must be complete before stage 4 reads them.
	Identity identityContext
	// OAuthToken is the validated bearer retained for the request. On OAuth
	// servers upstreamFilter forwards it to the upstream MCP resource; on
	// non-OAuth servers the Authorization header is stripped instead.
	OAuthToken string

	// Set by stage 4 (AuthzFilter). Policy and Identity must not change after this.
	Decision policypkg.Decision
	// Authorization remains separate from operational denials during dispatch.
	AuthorizationDecision policypkg.Decision
	AuthorizationObserved bool

	// SkipAudit is set by a filter that writes a terminal response that bypasses
	// authentication and authorization entirely (e.g. OAuth metadata early-exit in
	// policyFilter). When true, emitAuditFromExchange does nothing, because no
	// identity or authorization decision was established for the request.
	SkipAudit bool

	// Idempotency records the opt-in Idempotency-Key outcome for audit.
	Idempotency idempotencyAudit
}

// newExchange initialises an Exchange for a fresh inbound request.
//
// The default decision is deny: every terminal path in the pipeline sets an
// explicit decision (authzFilter for tool calls and the non-tool passthrough,
// authFilter on OAuth failure, policyFilter for OAuth metadata). A deny default
// means any request that reaches upstream or audit without an explicit decision
// — a filter-ordering bug or a future unhandled stage — fails closed instead of
// being proxied as allowed.
func newExchange(w http.ResponseWriter, r *http.Request, defaultPolicyVersion string) *Exchange {
	return &Exchange{
		W:            &statusRecorder{ResponseWriter: w, status: http.StatusOK},
		R:            r,
		OriginalPath: r.URL.Path,
		StartTime:    time.Now(),
		Decision:     policypkg.Deny(http.StatusForbidden, "undecided", defaultPolicyVersion),
	}
}
