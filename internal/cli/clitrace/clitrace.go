package clitrace

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"mcp-runtime/pkg/telemetry"
	"mcp-runtime/pkg/transport/httptrace"
)

const serviceName = "mcp-runtime-cli"

var (
	mu   sync.Mutex
	root trace.SpanContext
)

// Start opens the root span for this invocation and returns the function that
// ends it. Spans are exported when OTEL_EXPORTER_OTLP_ENDPOINT is set; without
// it the trace id is still generated and sent to the platform, whose spans
// then appear without a CLI root. Calling Start again replaces the trace.
func Start(command string) (end func()) {
	shutdown, err := telemetry.InitTracer(serviceName)
	if err != nil {
		shutdown = func(context.Context) error { return nil }
	}
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" || err != nil {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	}
	ctx, span := otel.Tracer("mcp-runtime/cli").Start(context.Background(), command)
	mu.Lock()
	root = trace.SpanContextFromContext(ctx)
	mu.Unlock()
	return func() {
		span.End()
		mu.Lock()
		root = trace.SpanContext{}
		mu.Unlock()
		_ = shutdown(context.Background())
	}
}

// TraceID returns the invocation's trace id, or "" before Start.
func TraceID() string {
	mu.Lock()
	defer mu.Unlock()
	if !root.IsValid() {
		return ""
	}
	return root.TraceID().String()
}

// Transport sends requests as part of the invocation's trace. Requests made
// before Start, or by a caller that already carries a span, are left alone.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return joinTransport{plain: base, traced: httptrace.TracedTransport(base)}
}

type joinTransport struct {
	plain, traced http.RoundTripper
}

func (t joinTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	mu.Lock()
	current := root
	mu.Unlock()
	if !current.IsValid() {
		return t.plain.RoundTrip(req)
	}
	if !trace.SpanContextFromContext(req.Context()).IsValid() {
		req = req.WithContext(trace.ContextWithSpanContext(req.Context(), current))
	}
	return t.traced.RoundTrip(req)
}
