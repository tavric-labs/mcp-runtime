package audit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcp-runtime/hack/prod-audit/internal/fixture"
	"mcp-runtime/hack/prod-audit/internal/server"
)

func fixtureEndpoint(t *testing.T, override string, response string) string {
	t.Helper()
	s := &server.Service{Store: fixture.NewStore(time.Now), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), JSONResponse: true}
	srv := s.NewMCPServer()
	if override != "" {
		mcp.AddTool(srv, &mcp.Tool{Name: override}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: response}}}, nil, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(h)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func TestFixtureAuditChecksActualToolResults(t *testing.T) {
	for _, tc := range []struct {
		name, override, response string
		write, ok                bool
	}{
		{"real-fixture", "", "", true, true},
		{"corrupt-echo", "audit_echo", `{"message":"wrong"}`, false, false},
		{"lost-record", "audit_record_get", `{"key":"probe","value":"wrong","revision":1}`, true, false},
		{"cleanup-did-not-happen", "audit_run_close", `{"closed":false}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := fixtureEndpoint(t, tc.override, tc.response)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := CheckFixture(ctx, FixtureConfig{Endpoint: endpoint, Write: tc.write}, io.Discard)
			if (err == nil) != tc.ok {
				t.Fatalf("audit err=%v, want success=%v", err, tc.ok)
			}
		})
	}
}
