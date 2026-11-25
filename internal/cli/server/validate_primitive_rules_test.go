package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-runtime/internal/blueprint"
)

func grantWithPrimitiveRules(t *testing.T, rules string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grant.yaml")
	body := `apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: docs-reader
spec:
  serverRef:
    name: docs
  maxTrust: low
  allowedSideEffects: [read]
  primitiveRules:
` + rules
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateGrantFileChecksPrimitiveRules(t *testing.T) {
	cfg := &blueprint.RegistryFile{Servers: []blueprint.ServerMetadata{{Name: "docs"}}}
	for name, tc := range map[string]struct {
		rules string
		want  string // substring of a fatal issue, "" for none
	}{
		"valid":          {"    - {kind: resource, pattern: \"ui://a/*\", decision: allow}\n    - {kind: prompt, pattern: review, decision: deny}\n", ""},
		"unknown kind":   {"    - {kind: tool, pattern: x, decision: allow}\n", "kind"},
		"empty pattern":  {"    - {kind: resource, pattern: \"\", decision: allow}\n", "pattern"},
		"mid wildcard":   {"    - {kind: resource, pattern: \"a*b\", decision: allow}\n", "trailing"},
		"bad decision":   {"    - {kind: skill, pattern: s, decision: maybe}\n", "decision"},
		"duplicate rule": {"    - {kind: prompt, pattern: p, decision: allow}\n    - {kind: prompt, pattern: p, decision: deny}\n", "duplicate"},
	} {
		t.Run(name, func(t *testing.T) {
			issues, err := validateGrantFile(grantWithPrimitiveRules(t, tc.rules), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var fatal []string
			for _, i := range issues {
				if i.fatal {
					fatal = append(fatal, i.message)
				}
			}
			if tc.want == "" {
				if len(fatal) != 0 {
					t.Fatalf("unexpected issues: %v", fatal)
				}
				return
			}
			if !strings.Contains(strings.ToLower(strings.Join(fatal, "\n")), tc.want) {
				t.Fatalf("want a fatal issue mentioning %q, got %v", tc.want, fatal)
			}
		})
	}
}

// Invalid rules must be reported even when the server is not in this metadata
// file, because the lookup returns early in that case.
func TestValidateGrantFileChecksPrimitiveRulesWithoutServerMetadata(t *testing.T) {
	issues, err := validateGrantFile(grantWithPrimitiveRules(t, "    - {kind: tool, pattern: x, decision: allow}\n"), &blueprint.RegistryFile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.fatal && strings.Contains(i.message, "primitiveRules") {
			return
		}
	}
	t.Fatalf("invalid primitive rule was not reported: %+v", issues)
}
