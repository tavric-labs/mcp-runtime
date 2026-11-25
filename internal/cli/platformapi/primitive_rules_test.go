package platformapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A grant manifest's primitiveRules must reach the platform API. If the CLI
// dropped them, a scoped grant would silently become an open one.
func TestApplyGrantSendsPrimitiveRules(t *testing.T) {
	var sent map[string]any
	client := &PlatformClient{
		baseURL: "https://platform.example.com", token: "token-1", apiPrefix: "/api/v1",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/runtime/grants" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		})},
	}
	manifest := filepath.Join(t.TempDir(), "grant.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: docs-reader
  namespace: mcp-servers-org
spec:
  serverRef:
    name: docs
  subject:
    agentID: agent-a
  maxTrust: low
  allowedSideEffects: [read]
  primitiveRules:
    - {kind: resource, pattern: "file:///docs/*", decision: allow}
    - {kind: prompt, pattern: deploy, decision: deny}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.ApplyAccessFromYAMLFile(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	rules, _ := sent["primitiveRules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("primitiveRules not sent: %v", sent)
	}
	first := rules[0].(map[string]any)
	if first["kind"] != "resource" || first["pattern"] != "file:///docs/*" || first["decision"] != "allow" {
		t.Fatalf("first rule = %v", first)
	}
}

func TestApplyGrantRejectsInvalidPrimitiveRulesBeforeSending(t *testing.T) {
	client := &PlatformClient{
		baseURL: "https://platform.example.com", token: "token-1", apiPrefix: "/api/v1",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatalf("an invalid grant must not be sent: %s", r.URL.Path)
			return nil, nil
		})},
	}
	manifest := filepath.Join(t.TempDir(), "grant.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: bad
  namespace: mcp-servers-org
spec:
  serverRef:
    name: docs
  subject:
    agentID: agent-a
  maxTrust: low
  allowedSideEffects: [read]
  primitiveRules:
    - {kind: tool, pattern: x, decision: allow}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.ApplyAccessFromYAMLFile(context.Background(), manifest); err == nil {
		t.Fatal("expected a validation error for kind \"tool\"")
	}
}
