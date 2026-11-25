package accessintent

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
	"mcp-runtime/pkg/httpx"
)

func TestReconcileJobsCarryRequestAndTrace(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("f981485280c3a8022f39bc5a26151755")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("4c87d0001fb18b21")
	if err != nil {
		t.Fatal(err)
	}
	traceState, err := trace.ParseTraceState("vendor=fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, err := baggage.NewMember("unrelated", "do-not-enqueue")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(t.Context(), bag)
	ctx = httpx.ContextWithRequestID(ctx, "access-req-1")
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: traceState,
	}))

	grantJob := newReconcileGrantJob(ctx, "grant-1", "rev-1")
	if grantJob.Kind() != KindReconcileGrant {
		t.Fatalf("grant job kind = %q, want %q", grantJob.Kind(), KindReconcileGrant)
	}
	if grantJob.GrantID != "grant-1" || grantJob.RevisionID != "rev-1" || grantJob.RequestID != "access-req-1" ||
		grantJob.Traceparent != "00-f981485280c3a8022f39bc5a26151755-4c87d0001fb18b21-01" || grantJob.Tracestate != "vendor=fixture" {
		t.Fatalf("unexpected grant job args: %+v", grantJob)
	}

	enrollJob := newReconcileEnrollmentJob(ctx, "op-1", "rev-e1")
	if enrollJob.Kind() != KindReconcileEnrollment {
		t.Fatalf("enrollment job kind = %q, want %q", enrollJob.Kind(), KindReconcileEnrollment)
	}
	if enrollJob.OperationID != "op-1" || enrollJob.RevisionID != "rev-e1" || enrollJob.RequestID != "access-req-1" ||
		enrollJob.Traceparent != grantJob.Traceparent || enrollJob.Tracestate != grantJob.Tracestate {
		t.Fatalf("unexpected enrollment job args: %+v", enrollJob)
	}

	cleanupJob := newReconcileAgentCleanupJob(ctx, "agt-1", "rev-c1")
	if cleanupJob.Kind() != KindReconcileAgentCleanup {
		t.Fatalf("cleanup job kind = %q, want %q", cleanupJob.Kind(), KindReconcileAgentCleanup)
	}
	if cleanupJob.AgentID != "agt-1" || cleanupJob.RevisionID != "rev-c1" || cleanupJob.RequestID != "access-req-1" ||
		cleanupJob.Traceparent != grantJob.Traceparent || cleanupJob.Tracestate != grantJob.Tracestate {
		t.Fatalf("unexpected cleanup job args: %+v", cleanupJob)
	}

	empty := newReconcileGrantJob(t.Context(), "grant-1", "rev-1")
	if empty.RequestID != "" || empty.Traceparent != "" || empty.Tracestate != "" {
		t.Fatalf("job fabricated request or trace context: %+v", empty)
	}
}

func TestDeterministicCertificateRequestName(t *testing.T) {
	csr := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	a := DeterministicCertificateRequestName("sess-uid-1", "srv-uid-1", csr)
	b := DeterministicCertificateRequestName("sess-uid-1", "srv-uid-1", csr)
	c := DeterministicCertificateRequestName("sess-uid-2", "srv-uid-1", csr)

	if a != b {
		t.Fatalf("expected deterministic name, got %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("expected distinct names for distinct sessions, got %q", a)
	}
	if !strings.HasPrefix(a, "enroll-") {
		t.Fatalf("expected enroll- prefix, got %q", a)
	}
	if len(a) > 63 {
		t.Fatalf("CR name %q exceeds 63-char Kubernetes limit (len=%d)", a, len(a))
	}
}

func TestValidateCSRSHA256RejectsSecretsAndInvalidHex(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := validateCSRSHA256(valid); err != nil {
		t.Fatalf("valid sha256 rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"short",
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN CERTIFICATE REQUEST-----",
		strings.Repeat("Z", 64),
		strings.Repeat("A", 64),
	} {
		if err := validateCSRSHA256(bad); err == nil {
			t.Errorf("expected invalid CSR digest %q to be rejected", bad)
		}
	}
}
