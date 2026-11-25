// Package accessintentworker runs River v0.51.0 workers in runtime-api that
// reconcile durable access-control intents (grants, adapter enrollments, and
// post-deactivation agent cleanup) against Kubernetes and Platform API.
package accessintentworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"go.opentelemetry.io/otel/propagation"

	"mcp-runtime/pkg/httpx"
)

const (
	// QueueSchema is the River PostgreSQL schema shared with platform-api.
	QueueSchema = "access_intent_queue"
	// QueueAccessControl is the River queue for access-control reconciliation jobs.
	QueueAccessControl = "access_control"

	// KindReconcileGrant reconciles one grant incarnation.
	KindReconcileGrant = "reconcile_grant"
	// KindReconcileEnrollment reconciles one adapter enrollment operation.
	KindReconcileEnrollment = "reconcile_enrollment"
	// KindReconcileAgentCleanup reconciles post-deactivation agent cleanup.
	KindReconcileAgentCleanup = "reconcile_agent_cleanup"

	// GrantIDAnnotation links a projected MCPAccessGrant CR to its immutable platform grant incarnation ID.
	GrantIDAnnotation = "mcpruntime.org/grant-id"
	// DesiredRevisionAnnotation records the durable intent revision projected into a CR.
	DesiredRevisionAnnotation = "mcpruntime.org/desired-revision-id"
	// SpecDigestAnnotation records the canonical spec digest projected into a CR.
	SpecDigestAnnotation = "mcpruntime.org/spec-digest"
)

// ReconcileGrantJob matches platform-api's reconcile_grant River job payload.
type ReconcileGrantJob struct {
	GrantID     string `json:"grantId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileGrantJob) Kind() string { return KindReconcileGrant }

// ReconcileEnrollmentJob matches platform-api's reconcile_enrollment River job payload.
type ReconcileEnrollmentJob struct {
	OperationID string `json:"operationId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileEnrollmentJob) Kind() string { return KindReconcileEnrollment }

// ReconcileAgentCleanupJob matches platform-api's reconcile_agent_cleanup River job payload.
type ReconcileAgentCleanupJob struct {
	AgentID     string `json:"agentId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileAgentCleanupJob) Kind() string { return KindReconcileAgentCleanup }

func contextWithJobTrace(ctx context.Context, requestID, traceparent, tracestate string) context.Context {
	if strings.TrimSpace(requestID) != "" {
		ctx = httpx.ContextWithRequestID(ctx, strings.TrimSpace(requestID))
	}
	carrier := propagation.MapCarrier{}
	if strings.TrimSpace(traceparent) != "" {
		carrier.Set("traceparent", strings.TrimSpace(traceparent))
	}
	if strings.TrimSpace(tracestate) != "" {
		carrier.Set("tracestate", strings.TrimSpace(tracestate))
	}
	if len(carrier) > 0 {
		ctx = propagation.TraceContext{}.Extract(ctx, carrier)
	}
	return ctx
}

// SpecDigest returns a stable sha256 digest of a grant's JSON spec payload.
func SpecDigest(spec json.RawMessage) string {
	var normalized any
	if len(spec) > 0 && json.Unmarshal(spec, &normalized) == nil {
		if canonical, err := json.Marshal(normalized); err == nil {
			sum := sha256.Sum256(canonical)
			return "sha256:" + hex.EncodeToString(sum[:])
		}
	}
	sum := sha256.Sum256(spec)
	return "sha256:" + hex.EncodeToString(sum[:])
}
