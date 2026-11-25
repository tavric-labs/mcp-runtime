package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v4"

	policypkg "mcp-runtime/pkg/policy"
)

// Every method an extension adds must be gated exactly as the registry says and
// carry a fixed metric label, so a new extension cannot silently bypass policy.
func TestExtensionRegistryMatchesPolicyAndMetrics(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, ext := range extensionRegistry {
		if !strings.Contains(ext.ID, "/") {
			t.Errorf("extension id %q must be {vendor}/{name}", ext.ID)
		}
		for _, m := range ext.Methods {
			if other, dup := seen[m.Method]; dup {
				t.Errorf("%s registered by %s and %s", m.Method, other, ext.ID)
			}
			seen[m.Method] = ext.ID
			if got := policypkg.IsPrimitiveMethod(m.Method); got != m.Gated {
				t.Errorf("%s: registry says gated=%v but policy says %v", m.Method, m.Gated, got)
			}
			if label := metricRPCMethod(m.Method); label != m.Method {
				t.Errorf("%s has no metric label (got %q)", m.Method, label)
			}
		}
	}
	for _, want := range []string{"skills/list", "skills/get", "resources/directory/read", "tasks/get", "tasks/update", "tasks/cancel"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("%s missing from the extension registry", want)
		}
	}
	if metricRPCMethod("example.test/unregistered") != "other" {
		t.Error("unregistered methods must stay in the bounded 'other' label")
	}
}

func TestMCPAppsUIResourcesFollowResourceRules(t *testing.T) {
	t.Parallel()
	runPipeline(t, resourceRules(
		policypkg.PrimitiveRule{Kind: "resource", Pattern: "ui://dashboard/*", Decision: "allow"},
	), []pipelineCase{
		{"app ui resource allowed", `{"id":1,"method":"resources/read","params":{"uri":"ui://dashboard/main.html"}}`, nil, 204, "", true},
		{"other ui resource denied", `{"id":2,"method":"resources/read","params":{"uri":"ui://admin/panel.html"}}`, nil, 403, "primitive_not_granted", false},
		{"app-originated call to a granted tool", `{"id":3,"method":"tools/call","params":{"name":"echo","_meta":{"ui":{"resourceUri":"ui://dashboard/main.html"}}}}`, nil, 204, "", true},
		{"app-originated call to an ungranted tool is denied", `{"id":4,"method":"tools/call","params":{"name":"delete_all","_meta":{"ui":{"resourceUri":"ui://dashboard/main.html"}}}}`, nil, 403, "tool_not_granted", false},
	})
}

// Extension capabilities and _meta keys must reach the upstream unchanged for
// both negotiation styles.
func TestExtensionPayloadsReachUpstreamUnchanged(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	var got []string
	var reached atomic.Int32
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		b := make([]byte, 1<<16)
		n, _ := r.Body.Read(b)
		got = append(got, string(b[:n]))
		w.WriteHeader(http.StatusNoContent)
	})
	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human-1", "azp": "client-1",
		"team_id": "team-acme", "sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	})
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"extensions":{"io.modelcontextprotocol/ui":{"mimeTypes":["text/html;profile=mcp-app"]}}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/skills":{}}}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","_meta":{"ui":{"resourceUri":"ui://x/y.html","csp":{"connectDomains":["https://a.example"]}}}}}`,
	}
	for _, body := range bodies {
		req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		proxy.handleGateway(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d for %s: %s", rec.Code, body, rec.Body.String())
		}
	}
	if int(reached.Load()) != len(bodies) {
		t.Fatalf("upstream saw %d of %d requests", reached.Load(), len(bodies))
	}
	for i, body := range bodies {
		if got[i] != body {
			t.Errorf("body %d altered in transit:\n got %s\nwant %s", i, got[i], body)
		}
	}
}
