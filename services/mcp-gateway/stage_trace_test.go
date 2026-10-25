package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestPipelineStageTraceContextsAreRestored(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	ctx, root := provider.Tracer("test").Start(context.Background(), "request")
	s := minimalServer()
	pipeline := s.buildPipeline()
	pipeline.Upstream = FilterFunc[PermittedRequest, AuthorizedRequest](func(permitted PermittedRequest) (AuthorizedRequest, *FilterResponse) {
		in := permitted.request
		active := trace.SpanFromContext(in.R.Context()).SpanContext()
		if active.SpanID() == root.SpanContext().SpanID() || active.TraceID() != root.SpanContext().TraceID() {
			t.Fatal("upstream did not receive its stage context")
		}
		return in, forwardedResponse(in.Decision)
	})
	ex := newExchange(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx), "test")
	pipeline.Run(ex)
	if trace.SpanFromContext(ex.R.Context()).SpanContext().SpanID() != root.SpanContext().SpanID() {
		t.Fatal("stage context leaked into finalization")
	}
	root.End()
	spans := recorder.Ended()
	want := []string{"gateway.inspect", "gateway.policy", "gateway.auth", "gateway.authz", "gateway.upstream", "request"}
	if len(spans) != len(want) {
		t.Fatalf("got %d spans, want %d", len(spans), len(want))
	}
	for i, name := range want {
		if spans[i].Status().Code == codes.Error {
			t.Fatalf("successful stage %s marked failed", name)
		}
		if spans[i].Name() != name {
			t.Fatalf("span %d = %q, want %q", i, spans[i].Name(), name)
		}
		if i < len(want)-1 && spans[i].Parent().SpanID() != root.SpanContext().SpanID() {
			t.Fatalf("%s is not a child of the request span", name)
		}
	}
}

func TestGatewayTraceStopsAtFailedAuthentication(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	ctx, root := provider.Tracer("test").Start(context.Background(), "request")
	issuer := newTestJWTIssuer(t)
	server := newTestGatewayServer(t, oauthPolicy(issuer.url), func(http.ResponseWriter, *http.Request) {
		t.Error("denied call reached upstream")
	})
	request := httptest.NewRequest(http.MethodPost, "http://gateway/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"password":"private-value"}}}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	server.handleGateway(response, request)
	root.End()
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	spans := recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("expected inspect, policy, auth and request spans, got %d", len(spans))
	}
	for i, name := range []string{"gateway.inspect", "gateway.policy", "gateway.auth"} {
		span := spans[i]
		if span.Name() != name || span.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Fatalf("unexpected stage trace: %s", span.Name())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private-value") {
				t.Fatal("payload leaked into trace")
			}
		}
	}
	if spans[2].Status().Code != codes.Error || spans[3].Status().Code != codes.Error {
		t.Fatal("denial missing error status")
	}
}
