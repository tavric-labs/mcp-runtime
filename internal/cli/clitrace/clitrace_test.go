package clitrace

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var traceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-[0-9a-f]{16}-0[01]$`)

func captureHeader(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func TestTransportJoinsTheInvocationTrace(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	end := Start("mcp-runtime grant create")
	t.Cleanup(end)
	server, got := captureHeader(t)

	client := &http.Client{Transport: Transport(nil)}
	for i := 0; i < 2; i++ {
		resp, err := client.Get(server.URL) // background context, as the CLI uses
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		match := traceparent.FindStringSubmatch(*got)
		if match == nil {
			t.Fatalf("traceparent = %q, want a W3C header", *got)
		}
		if match[1] != TraceID() {
			t.Fatalf("request trace id = %s, want the invocation's %s", match[1], TraceID())
		}
	}
}

func TestTransportSendsNothingBeforeStart(t *testing.T) {
	server, got := captureHeader(t)
	client := &http.Client{Transport: Transport(nil)}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if *got != "" {
		t.Fatalf("traceparent = %q, want none when no trace was started", *got)
	}
}

func TestEachInvocationGetsItsOwnTrace(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	end := Start("first")
	first := TraceID()
	end()
	end = Start("second")
	t.Cleanup(end)
	if first == "" || TraceID() == "" || first == TraceID() {
		t.Fatalf("trace ids %q then %q, want two distinct non-empty ids", first, TraceID())
	}
}
