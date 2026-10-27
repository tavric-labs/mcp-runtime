package kafkatrace

import (
	"context"
	"testing"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestKafkaTraceHeadersRoundTrip(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(previous)
	})

	traceID := trace.TraceID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	spanID := trace.SpanID{8, 7, 6, 5, 4, 3, 2, 1}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	headers := InjectKafkaHeaders(ctx, []kafka.Header{{Key: "existing", Value: []byte("value")}})
	if got := kafkaHeaderValue(headers, "traceparent"); got == "" {
		t.Fatalf("InjectKafkaHeaders() missing traceparent: %#v", headers)
	}

	extracted := trace.SpanContextFromContext(ExtractKafkaHeaders(context.Background(), headers))
	if extracted.TraceID() != traceID {
		t.Fatalf("extracted trace ID = %s, want %s", extracted.TraceID(), traceID)
	}
	if !extracted.IsRemote() {
		t.Fatal("extracted span context should be remote")
	}
}

func kafkaHeaderValue(headers []kafka.Header, key string) string {
	for _, header := range headers {
		if header.Key == key {
			return string(header.Value)
		}
	}
	return ""
}
