package teamintent

import (
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
	"mcp-runtime/pkg/httpx"
)

func TestReconcileJobCarriesRequestAndTrace(t *testing.T) {
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
	ctx = httpx.ContextWithRequestID(ctx, "team-request-a")
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: traceState,
	}))

	got := newReconcileJob(ctx, "team-1", "revision-2")
	want := reconcileJob{
		TeamID: "team-1", RevisionID: "revision-2", RequestID: "team-request-a",
		Traceparent: "00-f981485280c3a8022f39bc5a26151755-4c87d0001fb18b21-01",
		Tracestate:  "vendor=fixture",
	}
	if got != want {
		t.Fatalf("job args = %+v, want %+v", got, want)
	}
	if job := newReconcileJob(t.Context(), "team-1", "revision-2"); job.RequestID != "" || job.Traceparent != "" || job.Tracestate != "" {
		t.Fatalf("job fabricated request or trace context: %+v", job)
	}
}
