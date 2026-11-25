// Package server exposes a synthetic fixture through the official MCP SDK.
// Runtime's private gateway is the production authentication and policy boundary.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcp-runtime/hack/prod-audit/internal/fixture"
)

const Version = "0.1.1"

type Service struct {
	Store        *fixture.Store
	Logger       *slog.Logger
	JSONResponse bool
}

type Empty struct{}
type EchoArgs struct {
	Message string `json:"message" jsonschema:"Synthetic marker to echo, at most 4096 bytes. Do not send credentials or personal data."`
}
type OpenArgs struct {
	Label      string `json:"label,omitempty" jsonschema:"Synthetic test label, at most 80 bytes."`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"Retention from 1 to 3600 seconds; default 900."`
}
type RunArgs struct {
	RunID string `json:"run_id" jsonschema:"Exact run identifier returned by audit_run_open."`
}
type KeyArgs struct {
	RunID string `json:"run_id"`
	Key   string `json:"key"`
}
type PutArgs struct {
	RunID            string  `json:"run_id"`
	Key              string  `json:"key"`
	Value            string  `json:"value" jsonschema:"Synthetic text, at most 4096 bytes."`
	ExpectedRevision *uint64 `json:"expected_revision,omitempty" jsonschema:"Optional compare-and-swap revision; zero requires a missing record."`
}
type DeleteArgs struct {
	RunID            string  `json:"run_id"`
	Key              string  `json:"key"`
	ExpectedRevision *uint64 `json:"expected_revision,omitempty"`
}
type WaitArgs struct {
	Milliseconds int `json:"milliseconds" jsonschema:"Delay from 0 to 2000 milliseconds; cancellation is respected."`
	Steps        int `json:"steps,omitempty" jsonschema:"Progress steps from 1 to 10, default 1. Supply a progress token and use SSE to receive progress."`
}

func textResult(value any) *mcp.CallToolResult {
	raw, err := json.Marshal(value)
	if err != nil {
		return failure("cannot encode result")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

func failure(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func toolError(err error) (*mcp.CallToolResult, any, error) { return failure(err.Error()), nil, nil }

func register[A any](s *Service, srv *mcp.Server, name, description string, read, destructive, idempotent bool, handler func(context.Context, *mcp.CallToolRequest, A) (*mcp.CallToolResult, any, error)) {
	closed := false
	mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint: read, DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &closed,
	}}, func(ctx context.Context, req *mcp.CallToolRequest, args A) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		result, output, err := handler(ctx, req, args)
		outcome := "success"
		if err != nil || (result != nil && result.IsError) {
			outcome = "error"
		}
		// Deliberately omit arguments, payloads, headers, bearer tokens and run IDs.
		s.Logger.Info("audit tool", "tool", name, "outcome", outcome, "duration_ms", time.Since(start).Milliseconds())
		return result, output, err
	})
}

func (s *Service) NewMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "prod-audit", Version: Version}, &mcp.ServerOptions{
		Instructions: "Dedicated production platform test fixture. Use synthetic data only. Open a separate run for each test and always close it. Never use LinkedIn, Cully, or customer MCP servers as test targets. This server cannot inspect or mutate external systems.",
	})
	register(s, srv, "audit_inspect", "Inspect this fixture's version, transport mode, memory limits and active data counts.", true, false, true,
		func(context.Context, *mcp.CallToolRequest, Empty) (*mcp.CallToolResult, any, error) {
			return textResult(map[string]any{"server": "prod-audit", "version": Version, "transport": "streamable-http", "json_response": s.JSONResponse,
				"state": s.Store.Inspect(), "max_runs": fixture.MaxRuns, "max_records_per_run": fixture.MaxRecords,
				"max_value_bytes": fixture.MaxValueBytes, "max_ttl_seconds": int(fixture.MaxTTL.Seconds()), "authentication_boundary": "runtime_gateway"}), nil, nil
		})
	register(s, srv, "audit_echo", "Echo a bounded synthetic marker for routing, policy and analytics verification.", true, false, true,
		func(_ context.Context, _ *mcp.CallToolRequest, a EchoArgs) (*mcp.CallToolResult, any, error) {
			if len(a.Message) > fixture.MaxValueBytes {
				return toolError(fmt.Errorf("message exceeds 4096 bytes"))
			}
			return textResult(map[string]string{"message": a.Message}), nil, nil
		})
	register(s, srv, "audit_run_open", "Create an isolated synthetic test run that expires automatically. Close it after testing.", false, false, false,
		func(_ context.Context, _ *mcp.CallToolRequest, a OpenArgs) (*mcp.CallToolResult, any, error) {
			if a.TTLSeconds < 0 || a.TTLSeconds > 3600 {
				return toolError(fmt.Errorf("ttl_seconds must be 1 to 3600, or zero for the default"))
			}
			run, err := s.Store.Open(a.Label, time.Duration(a.TTLSeconds)*time.Second)
			if err != nil {
				return toolError(err)
			}
			return textResult(run), nil, nil
		})
	register(s, srv, "audit_record_put", "Write a bounded synthetic record in one run. An optional expected_revision enables race tests.", false, false, true,
		func(_ context.Context, _ *mcp.CallToolRequest, a PutArgs) (*mcp.CallToolResult, any, error) {
			record, err := s.Store.Put(a.RunID, a.Key, a.Value, a.ExpectedRevision)
			if err != nil {
				return toolError(err)
			}
			return textResult(record), nil, nil
		})
	register(s, srv, "audit_record_get", "Read one synthetic record from an exact test run.", true, false, true,
		func(_ context.Context, _ *mcp.CallToolRequest, a KeyArgs) (*mcp.CallToolResult, any, error) {
			record, err := s.Store.Get(a.RunID, a.Key)
			if err != nil {
				return toolError(err)
			}
			return textResult(record), nil, nil
		})
	register(s, srv, "audit_record_delete", "Delete one synthetic record from an exact test run; repeat deletion is harmless.", false, true, true,
		func(_ context.Context, _ *mcp.CallToolRequest, a DeleteArgs) (*mcp.CallToolResult, any, error) {
			removed, err := s.Store.Delete(a.RunID, a.Key, a.ExpectedRevision)
			if err != nil {
				return toolError(err)
			}
			return textResult(map[string]bool{"deleted": removed}), nil, nil
		})
	register(s, srv, "audit_run_close", "Delete every synthetic record in one exact run. It cannot close other runs or real server data.", false, true, true,
		func(_ context.Context, _ *mcp.CallToolRequest, a RunArgs) (*mcp.CallToolResult, any, error) {
			removed, err := s.Store.Close(a.RunID)
			if err != nil {
				return toolError(err)
			}
			return textResult(map[string]bool{"closed": removed}), nil, nil
		})
	register(s, srv, "audit_wait", "Bounded delay with cancellation and optional SSE progress; no data changes.", true, false, true, s.wait)
	register(s, srv, "audit_fail", "Return a deliberate tool-level error to verify error attribution and audit events; no data changes.", true, false, true,
		func(context.Context, *mcp.CallToolRequest, Empty) (*mcp.CallToolResult, any, error) {
			return failure("prod-audit: deliberate test failure"), nil, nil
		})
	srv.AddResource(&mcp.Resource{URI: "audit://guide", Name: "audit-guide", MIMEType: "text/plain", Description: "Fixture use and cleanup rules."},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "audit://guide", MIMEType: "text/plain", Text: "Use only synthetic data. Open a run, exercise allowed and denied tools through the Runtime gateway, verify the corresponding audit/analytics events, and close the run. Expired data is swept every 30 seconds. No external calls, shell, filesystem or Kubernetes access are available."}}}, nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "audit_plan", Description: "Plan a bounded production audit using prod-audit only."},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Audit Runtime using prod-audit only: inspect; verify anonymous denial; verify read/write/destructive tool grants and an ungranted denial; open an isolated run; put/get/CAS/delete synthetic data; check cancellation and deliberate errors; verify session revocation and analytics/trace correlation; close the run and retire audit-created access fixtures. Record what was observed and what was skipped. Never test against LinkedIn or Cully."}}}}, nil
		})
	return srv
}

func (s *Service) wait(ctx context.Context, req *mcp.CallToolRequest, a WaitArgs) (*mcp.CallToolResult, any, error) {
	if a.Milliseconds < 0 || a.Milliseconds > 2000 || a.Steps < 0 || a.Steps > 10 {
		return toolError(fmt.Errorf("milliseconds must be 0 to 2000 and steps 1 to 10"))
	}
	if a.Steps == 0 {
		a.Steps = 1
	}
	start := time.Now()
	for i := 1; i <= a.Steps; i++ {
		deadline := start.Add(time.Duration(a.Milliseconds) * time.Millisecond * time.Duration(i) / time.Duration(a.Steps))
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
		if !s.JSONResponse && req != nil && req.Session != nil && req.Params != nil && req.Params.GetProgressToken() != nil {
			if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), Progress: float64(i), Total: float64(a.Steps), Message: "synthetic audit progress"}); err != nil {
				return nil, nil, err
			}
		}
	}
	return textResult(map[string]any{"completed": true, "milliseconds": a.Milliseconds, "steps": a.Steps}), nil, nil
}

// Handler is intentionally private behind Runtime's gateway in production.
// The backend has no platform admin or Kubernetes credentials.
func (s *Service) Handler() http.Handler {
	srv := s.NewMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: s.JSONResponse, Logger: s.Logger})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"server":"prod-audit","version":%q}`, Version)
	})
	mcpPath, err := ConfiguredMCPPath(os.Getenv("MCP_PATH"))
	if err != nil {
		panic(err)
	} // main validates configuration before starting.
	mux.Handle(mcpPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		handler.ServeHTTP(w, r)
	}))
	return mux
}

func ConfiguredMCPPath(raw string) (string, error) {
	if raw == "" {
		return "/mcp", nil
	}
	if !strings.HasPrefix(raw, "/") || raw == "/" || raw == "/health" || path.Clean(raw) != raw || strings.ContainsAny(raw, " {}?#\r\n\t") {
		return "", fmt.Errorf("MCP_PATH must be an absolute clean path other than / or /health")
	}
	return raw, nil
}
