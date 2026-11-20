package access

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".c2lnbmF0dXJl"
}

func writeTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSubjectFromTokenReturnsOnlySub(t *testing.T) {
	token := testJWT(t, map[string]any{"sub": "res-youtube:123", "email": "a@example.com", "iss": "x"})
	got, err := subjectFromToken(token)
	if err != nil {
		t.Fatalf("subjectFromToken() error = %v", err)
	}
	if got != "res-youtube:123" {
		t.Fatalf("subject = %q, want res-youtube:123", got)
	}
}

func TestSubjectFromTokenRejectsBadTokens(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"not a jwt":   "opaque-token",
		"two parts":   "a.b",
		"bad payload": "a.!!!.c",
		"no sub":      testJWT(t, map[string]any{"iss": "x"}),
		"empty sub":   testJWT(t, map[string]any{"sub": "  "}),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := subjectFromToken(token)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), token) && token != "" {
				t.Fatalf("error leaks the token: %v", err)
			}
		})
	}
}

func TestResolveTokenSubjectSources(t *testing.T) {
	token := testJWT(t, map[string]any{"sub": "user-1"})
	file := writeTokenFile(t, token)
	t.Setenv("TEST_EXPLAIN_TOKEN", token)

	if got, err := resolveTokenSubject(file, ""); err != nil || got != "user-1" {
		t.Fatalf("file: got %q, %v", got, err)
	}
	if got, err := resolveTokenSubject("", "TEST_EXPLAIN_TOKEN"); err != nil || got != "user-1" {
		t.Fatalf("env: got %q, %v", got, err)
	}
	if got, err := resolveTokenSubject("", ""); err != nil || got != "" {
		t.Fatalf("none: got %q, %v", got, err)
	}
	if _, err := resolveTokenSubject(file, "TEST_EXPLAIN_TOKEN"); err == nil {
		t.Fatal("expected error when both token sources are set")
	}
	if _, err := resolveTokenSubject("", "TEST_EXPLAIN_TOKEN_UNSET"); err == nil {
		t.Fatal("expected error for unset env var")
	}
}

func TestExplainUsesTokenSubjectAndNeverPrintsToken(t *testing.T) {
	doc := explainTestPolicy(t, "allow")
	policyFile := writeExplainPolicy(t, doc)
	token := testJWT(t, map[string]any{"sub": "alice", "email": "secret@example.com"})
	tokenFile := writeTokenFile(t, token)
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		err := mgr.ExplainAccess(explainOptions{
			Server: "workspace-assistant", Namespace: "mcp-servers-org", TokenFile: tokenFile,
			Tool: "write-file", RPCMethod: "tools/call", PolicyFile: policyFile, JSON: asJSON,
		}, &out)
		if err != nil {
			t.Fatalf("ExplainAccess(json=%v) error = %v", asJSON, err)
		}
		text := out.String()
		for _, leak := range []string{token, "secret@example.com"} {
			if strings.Contains(text, leak) {
				t.Fatalf("output leaks %q:\n%s", leak, text)
			}
		}
		if !strings.Contains(text, "alice") || !strings.Contains(text, "token") {
			t.Fatalf("output should name the subject and its source:\n%s", text)
		}
		if !strings.Contains(text, "gateway") {
			t.Fatalf("output should note the gateway evaluates the token sub:\n%s", text)
		}
	}
}

func TestExplainRejectsTokenWithHumanFlag(t *testing.T) {
	tokenFile := writeTokenFile(t, testJWT(t, map[string]any{"sub": "alice"}))
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	err := mgr.ExplainAccess(explainOptions{
		Server: "workspace-assistant", Namespace: "mcp-servers-org", TokenFile: tokenFile, HumanID: "bob",
		Tool: "write-file", RPCMethod: "tools/call", PolicyFile: "unused",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--human") {
		t.Fatalf("error = %v, want a --human conflict error", err)
	}
}

func TestExplainSchemaVersionErrorNamesCLIAndMatchingRelease(t *testing.T) {
	oldPolicy := `{"schema_version":"v99","revision":"r"}`
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(oldPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	SetCLIVersion("v0.5.2")
	t.Cleanup(func() { SetCLIVersion("") })
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	err := mgr.ExplainAccess(explainOptions{
		Server: "workspace-assistant", Namespace: "mcp-servers-org", HumanID: "alice",
		Tool: "write-file", RPCMethod: "tools/call", PolicyFile: path,
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"older than the platform policy", "v0.5.2", "v99", "matching release"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "mcp-runtime update") {
		t.Fatalf("error must not suggest `mcp-runtime update`: %q", msg)
	}
}

func TestInitGrantManifestSubjectFromTokenFile(t *testing.T) {
	tokenFile := writeTokenFile(t, testJWT(t, map[string]any{"sub": "res-youtube:123"}))
	output := filepath.Join(t.TempDir(), "grant.yaml")
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name: "yt", Namespace: "mcp-servers-org", Server: "youtube", Output: output,
		SubjectFromTokenFile: tokenFile, Tools: []string{"youtube_get_channel"},
	})
	if err != nil {
		t.Fatalf("InitGrantManifest() error = %v", err)
	}
	body, _ := os.ReadFile(output)
	if !strings.Contains(string(body), "humanID: res-youtube:123") {
		t.Fatalf("manifest missing token subject:\n%s", body)
	}

	err = mgr.InitGrantManifest(accessManifestInitOptions{
		Name: "yt", Namespace: "mcp-servers-org", Server: "youtube", Output: output, Force: true,
		SubjectFromTokenFile: tokenFile, HumanID: "other",
	})
	if err == nil || !strings.Contains(err.Error(), "--human-id") {
		t.Fatalf("error = %v, want --human-id conflict", err)
	}
}
