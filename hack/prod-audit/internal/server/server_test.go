package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcp-runtime/hack/prod-audit/internal/fixture"
)

func newService(jsonResponse bool, logOutput io.Writer) *Service {
	return &Service{Store: fixture.NewStore(time.Now), Logger: slog.New(slog.NewJSONHandler(logOutput, nil)), JSONResponse: jsonResponse}
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any, wantError bool) map[string]any {
	t.Helper()
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s error=%v want=%v: %+v", name, result.IsError, wantError, result.Content)
	}
	if wantError {
		return nil
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s: missing content", name)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestHTTPProtocolLifecycleJSONAndSSE(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		name := "sse"
		if jsonResponse {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			s := newService(jsonResponse, &logs)
			httpServer := httptest.NewServer(s.Handler())
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "audit-test", Version: "1"}, nil)
			cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			tools, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 9 {
				t.Fatalf("tools=%d", len(tools.Tools))
			}
			inspected := call(t, cs, "audit_inspect", Empty{}, false)
			if inspected["json_response"] != jsonResponse {
				t.Fatalf("wrong transport: %+v", inspected)
			}
			marker := "synthetic-do-not-log-payload"
			if got := call(t, cs, "audit_echo", EchoArgs{Message: marker}, false); got["message"] != marker {
				t.Fatal(got)
			}
			a := call(t, cs, "audit_run_open", OpenArgs{Label: "test-only", TTLSeconds: 30}, false)["run_id"].(string)
			b := call(t, cs, "audit_run_open", OpenArgs{TTLSeconds: 30}, false)["run_id"].(string)
			zero := uint64(0)
			put := call(t, cs, "audit_record_put", PutArgs{RunID: a, Key: "probe", Value: marker, ExpectedRevision: &zero}, false)
			if put["revision"] != float64(1) {
				t.Fatalf("revision: %v", put)
			}
			call(t, cs, "audit_record_get", KeyArgs{RunID: b, Key: "probe"}, true)
			if got := call(t, cs, "audit_record_get", KeyArgs{RunID: a, Key: "probe"}, false); got["value"] != marker {
				t.Fatal(got)
			}
			call(t, cs, "audit_record_put", PutArgs{RunID: a, Key: "probe", Value: "conflict", ExpectedRevision: &zero}, true)
			call(t, cs, "audit_record_delete", DeleteArgs{RunID: a, Key: "probe"}, false)
			call(t, cs, "audit_record_get", KeyArgs{RunID: a, Key: "probe"}, true)
			call(t, cs, "audit_run_close", RunArgs{RunID: a}, false)
			call(t, cs, "audit_run_close", RunArgs{RunID: a}, false)
			call(t, cs, "audit_run_close", RunArgs{RunID: b}, false)
			call(t, cs, "audit_fail", Empty{}, true)
			call(t, cs, "audit_wait", WaitArgs{Milliseconds: 1, Steps: 2}, false)
			call(t, cs, "audit_wait", WaitArgs{Milliseconds: 2001}, true)
			call(t, cs, "audit_echo", EchoArgs{Message: strings.Repeat("x", 4097)}, true)
			if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "audit://guide"}); err != nil {
				t.Fatal(err)
			}
			if _, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "audit_plan"}); err != nil {
				t.Fatal(err)
			}
			if stats := s.Store.Inspect(); stats.Runs != 0 || stats.Records != 0 {
				t.Fatalf("leaked fixtures: %+v", stats)
			}
			if strings.Contains(logs.String(), marker) || strings.Contains(logs.String(), a) {
				t.Fatal("payload or run identifier leaked into logs")
			}
		})
	}
}

func TestWaitHonorsCancellation(t *testing.T) {
	s := newService(false, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, _, err := s.wait(ctx, nil, WaitArgs{Milliseconds: 2000}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("cancellation waited for the delay")
	}
}

func TestRuntimeProvidedMCPPathIsServed(t *testing.T) {
	t.Setenv("MCP_PATH", "/prod-audit/mcp")
	s := newService(true, io.Discard)
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "route-test", Version: "1"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/prod-audit/mcp", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("Runtime's injected MCP_PATH is not served: %v", err)
	}
	defer cs.Close()
	call(t, cs, "audit_echo", EchoArgs{Message: "route-probe"}, false)
}

func TestSSEProgressIsDeliveredOnTheRequestStream(t *testing.T) {
	s := newService(false, io.Discard)
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	progress := make(chan float64, 4)
	c := mcp.NewClient(&mcp.Implementation{Name: "progress-test", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) { progress <- r.Params.Progress },
	})
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	p := &mcp.CallToolParams{Name: "audit_wait", Arguments: WaitArgs{Milliseconds: 30, Steps: 3}}
	p.SetProgressToken("synthetic-progress")
	if r, err := cs.CallTool(ctx, p); err != nil || r.IsError {
		t.Fatalf("wait: %v %v", r, err)
	}
	for want := 1.0; want <= 3; want++ {
		select {
		case got := <-progress:
			if got != want {
				t.Fatalf("progress=%v, want=%v", got, want)
			}
		case <-ctx.Done():
			t.Fatal("missing progress notification")
		}
	}
}

func TestBodySizeIsBounded(t *testing.T) {
	s := newService(true, io.Discard)
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat(" ", 40<<10)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", w.Code, w.Body.String())
	}
}
