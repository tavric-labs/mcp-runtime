package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRPCInspectionExtractsMethodAndTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, method, tool string
		toolCall, indeterminate  bool
	}{
		{name: "tool call", body: `{"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"}}}`, method: "tools/call", tool: "echo", toolCall: true},
		{name: "list tools", body: `{"method":"tools/list"}`, method: "tools/list"},
		{name: "notification", body: `{"method":"notifications/initialized"}`, method: "notifications/initialized"},
		{name: "missing tool", body: `{"method":"tools/call","params":{}}`, method: "tools/call", toolCall: true},
		{name: "invalid tool type", body: `{"method":"tools/call","params":{"name":7}}`, method: "tools/call", toolCall: true},
		{name: "empty body", indeterminate: true},
		{name: "broken JSON", body: `{"method":`, indeterminate: true},
		{name: "missing method", body: `{}`, indeterminate: true},
		{name: "blank method", body: `{"method":"  "}`, indeterminate: true},
		{name: "invalid method type", body: `{"method":3}`, indeterminate: true},
		{name: "array", body: `[{"method":"tools/call"}]`, indeterminate: true},
		{name: "trailing document", body: `{"method":"tools/list"}{}`, indeterminate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := inspectRPCBody([]byte(tc.body))
			if got.Method() != tc.method || got.ToolName() != tc.tool || got.IsToolCall() != tc.toolCall || got.Indeterminate() != tc.indeterminate {
				t.Fatalf("inspection=%+v", got)
			}
			if tc.indeterminate && (got.FailureReason() != "rpc_inspection_failed" || !got.IsRPCAttempt()) {
				t.Fatalf("unparseable RPC attempt lost its denial/audit classification: %+v", got)
			}
		})
	}
}

func TestRPCInspectionEnforcesBodySizeAndPreservesAcceptedBody(t *testing.T) {
	t.Parallel()
	const payload = `{"method":"tools/call","params":{"name":"echo"}}`
	for _, size := range []int{maxRPCBodyBytes - 1, maxRPCBodyBytes, maxRPCBodyBytes + 1} {
		for _, chunked := range []bool{false, true} {
			name := "known length"
			if chunked {
				name = "chunked"
			}
			t.Run(name+"/"+strconv.Itoa(size), func(t *testing.T) {
				t.Parallel()
				body := payload + strings.Repeat(" ", size-len(payload))
				reader := strings.NewReader(body)
				r := httptest.NewRequest(http.MethodPost, "/mcp", reader)
				if chunked {
					r.ContentLength = -1
				}
				got := inspectRPCRequest(r)
				wantRejected := size > maxRPCBodyBytes
				if got.Indeterminate() != wantRejected {
					t.Fatalf("size=%d chunked=%v inspection=%+v", size, chunked, got)
				}
				if wantRejected {
					if !got.IsRPCAttempt() {
						t.Fatal("oversized RPC attempt would be omitted from denial audit")
					}
					if !chunked && reader.Len() != size {
						t.Fatal("read body despite an oversized Content-Length")
					}
					return
				}
				forwarded, err := io.ReadAll(r.Body)
				if err != nil || string(forwarded) != body || got.ToolName() != "echo" {
					t.Fatalf("accepted body was not preserved: err=%v inspection=%+v", err, got)
				}
			})
		}
	}
}

func TestRPCInspectionBoundsReadsForUnknownLength(t *testing.T) {
	t.Parallel()
	reader := strings.NewReader(strings.Repeat("x", 2*maxRPCBodyBytes))
	r := httptest.NewRequest(http.MethodPost, "/mcp", reader)
	r.ContentLength = -1
	got := inspectRPCRequest(r)
	if !got.Indeterminate() || reader.Len() != maxRPCBodyBytes-1 {
		t.Fatalf("inspection=%+v unread=%d", got, reader.Len())
	}
}

type unreadableRPCBody struct{}

func (unreadableRPCBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (unreadableRPCBody) Close() error             { return nil }

func TestRPCInspectionClassifiesUninspectableRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, contentType string
		body                      io.ReadCloser
		wantIndeterminate, audit  bool
	}{
		{name: "GET does not read body", method: http.MethodGet, body: unreadableRPCBody{}},
		{name: "non JSON probe", method: http.MethodPost, contentType: "text/plain", body: unreadableRPCBody{}, wantIndeterminate: true},
		{name: "missing JSON body", method: http.MethodPost, contentType: "application/json", wantIndeterminate: true, audit: true},
		{name: "failed read", method: http.MethodPost, contentType: "application/json", body: unreadableRPCBody{}, wantIndeterminate: true, audit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(tc.method, "/mcp", nil)
			r.Header.Set("Content-Type", tc.contentType)
			r.Body, r.ContentLength = tc.body, -1
			got := inspectRPCRequest(r)
			if got.Indeterminate() != tc.wantIndeterminate || got.IsRPCAttempt() != tc.audit {
				t.Fatalf("inspection=%+v", got)
			}
		})
	}
}
