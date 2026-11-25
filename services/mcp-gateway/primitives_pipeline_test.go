package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	policypkg "mcp-runtime/pkg/policy"
)

// These tests drive the whole pipeline (authentication, inspection,
// authorization, proxy) for both protocol generations. The upstream counts the
// requests that reach it, so a denial that still forwarded would fail.

type pipelineCase struct {
	name         string
	body         string
	headers      map[string]string
	status       int
	reason       string // expected {"error": reason} for denials
	wantUpstream bool
}

func runPipeline(t *testing.T, mutate func(*policypkg.Document), cases []pipelineCase) {
	t.Helper()
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	if mutate != nil {
		mutate(policy)
	}
	var reached atomic.Int32
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human-1", "azp": "client-1",
		"team_id": "team-acme", "sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	})
	for _, tc := range cases {
		before := reached.Load()
		req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		proxy.handleGateway(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s: status=%d, want %d (%s)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
		if tc.reason != "" && !strings.Contains(rec.Body.String(), `"`+tc.reason+`"`) {
			t.Errorf("%s: body=%q, want reason %q", tc.name, rec.Body.String(), tc.reason)
		}
		if got := reached.Load() > before; got != tc.wantUpstream {
			t.Errorf("%s: reached upstream=%v, want %v", tc.name, got, tc.wantUpstream)
		}
	}
}

func resourceRules(rules ...policypkg.PrimitiveRule) func(*policypkg.Document) {
	return func(d *policypkg.Document) {
		d.SchemaVersion = policypkg.SchemaVersionPrimitiveRules
		d.Grants[0].PrimitiveRules = rules
	}
}

func TestPipelineGrantedReadsReachUpstreamInBothGenerations(t *testing.T) {
	runPipeline(t, nil, []pipelineCase{
		{"legacy resources/read", `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///a"}}`, nil, 204, "", true},
		{"legacy resources/subscribe", `{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":"file:///a"}}`, map[string]string{"Mcp-Session-Id": "x"}, 204, "", true},
		{"legacy prompts/get", `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"review"}}`, nil, 204, "", true},
		{"modern resources/read with headers", `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"file:///a","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, map[string]string{"Mcp-Method": "resources/read", "Mcp-Name": "file:///a", "MCP-Protocol-Version": "2026-07-28"}, 204, "", true},
		{"modern subscriptions/listen", `{"jsonrpc":"2.0","id":5,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true,"resourceSubscriptions":["file:///a"]}}}`, nil, 204, "", true},
		{"modern skills/get", `{"jsonrpc":"2.0","id":6,"method":"skills/get","params":{"uri":"skill://pdf/SKILL.md"}}`, nil, 204, "", true},
		{"modern tasks/get", `{"jsonrpc":"2.0","id":7,"method":"tasks/get","params":{"taskId":"t1"}}`, nil, 204, "", true},
		{"tool call unchanged", `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"echo"}}`, nil, 204, "", true},
		{"initialize stays open", `{"jsonrpc":"2.0","id":9,"method":"initialize","params":{}}`, nil, 204, "", true},
		{"server/discover stays open", `{"jsonrpc":"2.0","id":10,"method":"server/discover"}`, nil, 204, "", true},
		{"ping stays open", `{"jsonrpc":"2.0","id":11,"method":"ping"}`, nil, 204, "", true},
	})
}

func TestPipelineDeniedReadsNeverReachUpstream(t *testing.T) {
	runPipeline(t, resourceRules(
		policypkg.PrimitiveRule{Kind: "resource", Pattern: "file:///docs/*", Decision: "allow"},
		policypkg.PrimitiveRule{Kind: "resource", Pattern: "file:///docs/secret", Decision: "deny"},
		policypkg.PrimitiveRule{Kind: "prompt", Pattern: "review", Decision: "allow"},
	), []pipelineCase{
		{"allowed resource", `{"id":1,"method":"resources/read","params":{"uri":"file:///docs/a"}}`, nil, 204, "", true},
		{"resource outside rules", `{"id":2,"method":"resources/read","params":{"uri":"file:///etc/passwd"}}`, nil, 403, "primitive_not_granted", false},
		{"deny rule", `{"id":3,"method":"resources/read","params":{"uri":"file:///docs/secret"}}`, nil, 403, "primitive_denied", false},
		{"legacy subscribe outside rules", `{"id":4,"method":"resources/subscribe","params":{"uri":"file:///etc/passwd"}}`, nil, 403, "primitive_not_granted", false},
		{"modern listen with a bad uri", `{"id":5,"method":"subscriptions/listen","params":{"notifications":{"resourceSubscriptions":["file:///docs/a","file:///hr/1"]}}}`, nil, 403, "primitive_not_granted", false},
		{"prompt outside rules", `{"id":6,"method":"prompts/get","params":{"name":"deploy"}}`, nil, 403, "primitive_not_granted", false},
		{"completion reveals a denied prompt", `{"id":7,"method":"completion/complete","params":{"ref":{"type":"ref/prompt","name":"deploy"},"argument":{"name":"a","value":""}}}`, nil, 403, "primitive_not_granted", false},
		{"completion of an allowed prompt", `{"id":8,"method":"completion/complete","params":{"ref":{"type":"ref/prompt","name":"review"},"argument":{"name":"a","value":""}}}`, nil, 204, "", true},
		{"completion with no ref fails closed", `{"id":9,"method":"completion/complete","params":{}}`, nil, 403, "primitive_target_missing", false},
		{"read without a uri fails closed", `{"id":10,"method":"resources/read","params":{}}`, nil, 403, "primitive_target_missing", false},
	})
}

func TestPipelineRejectsHeaderBodyMismatchBeforeForwarding(t *testing.T) {
	runPipeline(t, nil, []pipelineCase{
		{"name header names a different tool", `{"id":1,"method":"tools/call","params":{"name":"echo"}}`, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "delete_all"}, 400, "header_mismatch", false},
		{"method header disagrees", `{"id":2,"method":"tools/call","params":{"name":"echo"}}`, map[string]string{"Mcp-Method": "tools/list"}, 400, "header_mismatch", false},
		{"name header names a different resource", `{"id":3,"method":"resources/read","params":{"uri":"file:///a"}}`, map[string]string{"Mcp-Method": "resources/read", "Mcp-Name": "file:///secret"}, 400, "header_mismatch", false},
		{"matching headers pass", `{"id":4,"method":"tools/call","params":{"name":"echo"}}`, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "echo"}, 204, "", true},
	})
}

func TestPipelineRequiresAGrantSessionAndIdentity(t *testing.T) {
	runPipeline(t, func(d *policypkg.Document) { d.Grants[0].HumanID = "someone-else" }, []pipelineCase{
		{"no matching grant", `{"id":1,"method":"resources/read","params":{"uri":"file:///a"}}`, nil, 403, "no_matching_grant", false},
		{"listing needs a grant too", `{"id":2,"method":"prompts/list"}`, nil, 403, "no_matching_grant", false},
		{"tasks need a grant", `{"id":3,"method":"tasks/get","params":{"taskId":"t"}}`, nil, 403, "no_matching_grant", false},
		{"lifecycle is unaffected", `{"id":4,"method":"initialize","params":{}}`, nil, 204, "", true},
	})
	runPipeline(t, func(d *policypkg.Document) { d.Sessions[0].Revoked = true }, []pipelineCase{
		{"revoked session", `{"id":1,"method":"resources/read","params":{"uri":"file:///a"}}`, nil, 401, "session_revoked", false},
	})
}
