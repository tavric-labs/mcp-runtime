package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

func inspectWithHeaders(body string, headers map[string]string) rpcInspection {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return inspectRPCRequest(r)
}

func TestMcpHeadersMustMatchTheBody(t *testing.T) {
	t.Parallel()
	call := `{"id":1,"method":"tools/call","params":{"name":"echo"}}`
	read := `{"id":1,"method":"resources/read","params":{"uri":"file:///a"}}`
	prompt := `{"id":1,"method":"prompts/get","params":{"name":"review"}}`
	for _, tc := range []struct {
		name, body string
		headers    map[string]string
		mismatch   bool
	}{
		{"legacy request sends no headers", call, nil, false},
		{"matching method and name", call, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "echo"}, false},
		{"method mismatch", call, map[string]string{"Mcp-Method": "tools/list"}, true},
		{"tool name mismatch", call, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "delete_all"}, true},
		{"resource uri match", read, map[string]string{"Mcp-Method": "resources/read", "Mcp-Name": "file:///a"}, false},
		{"resource uri mismatch", read, map[string]string{"Mcp-Method": "resources/read", "Mcp-Name": "file:///secret"}, true},
		{"prompt name match", prompt, map[string]string{"Mcp-Method": "prompts/get", "Mcp-Name": "review"}, false},
		{"prompt name mismatch", prompt, map[string]string{"Mcp-Name": "deploy"}, true},
		{"base64 name decodes before comparing", call, map[string]string{"Mcp-Name": "=?base64?ZWNobw==?="}, false},
		{"base64 name mismatch", call, map[string]string{"Mcp-Name": "=?base64?ZGVsZXRl?="}, true},
		{"name header is ignored for other methods", `{"id":1,"method":"tools/list"}`, map[string]string{"Mcp-Name": "whatever"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := inspectWithHeaders(tc.body, tc.headers)
			if tc.mismatch {
				if !got.Indeterminate() || got.FailureReason() != "header_mismatch" || !got.IsRPCAttempt() {
					t.Fatalf("mismatch not rejected: %+v", got)
				}
				return
			}
			if got.Indeterminate() {
				t.Fatalf("valid request rejected: %+v", got)
			}
		})
	}
}

func TestHeaderMismatchIsABadRequest(t *testing.T) {
	t.Parallel()
	in := authorizationExample()
	in.Inspection = failedRPCInspection(inspectionHeaderMismatch)
	got := authorizeRequest(in, time.Now())
	if got.Allowed || got.Status != http.StatusBadRequest || got.Reason != "header_mismatch" {
		t.Fatalf("decision=%+v", got)
	}
}

func TestCompletionIsGatedLikeTheItemItCompletes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	scoped := func(in *authorizationInput) {
		in.Policy.Grants[0].PrimitiveRules = nil
	}
	for _, tc := range []struct {
		name, body, reason string
		change             func(*authorizationInput)
	}{
		{"prompt ref with grant", `{"id":1,"method":"completion/complete","params":{"ref":{"type":"ref/prompt","name":"review"},"argument":{"name":"a","value":"x"}}}`, "allowed", scoped},
		{"resource ref with grant", `{"id":1,"method":"completion/complete","params":{"ref":{"type":"ref/resource","uri":"file:///docs/{p}"},"argument":{"name":"p","value":"x"}}}`, "allowed", scoped},
		{"no grant", `{"id":1,"method":"completion/complete","params":{"ref":{"type":"ref/prompt","name":"review"}}}`, "no_matching_grant", func(in *authorizationInput) { in.Policy.Grants[0].HumanID = "other" }},
		{"prompt outside rules", `{"id":1,"method":"completion/complete","params":{"ref":{"type":"ref/prompt","name":"deploy"}}}`, "primitive_not_granted", func(in *authorizationInput) {
			in.Policy.Grants[0].PrimitiveRules = []policypkg.PrimitiveRule{{Kind: "prompt", Pattern: "review", Decision: "allow"}}
		}},
		{"unknown ref type fails closed", `{"id":1,"method":"completion/complete","params":{"ref":{"type":"ref/other","name":"x"}}}`, "primitive_target_missing", scoped},
		{"missing ref fails closed", `{"id":1,"method":"completion/complete","params":{}}`, "primitive_target_missing", scoped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := authorizationExample()
			tc.change(&in)
			in.Inspection = inspectRPCBody([]byte(tc.body))
			if got := authorizeRequest(in, now); got.Reason != tc.reason {
				t.Fatalf("decision=%+v, want %s", got, tc.reason)
			}
		})
	}
}
