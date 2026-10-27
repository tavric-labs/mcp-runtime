package blueprint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestServerScopeLoadsNormalizedFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	doc := "version: v1\nservers:\n  - name: demo\n    image: demo\n    imageTag: v1\n    scope: \" Public \"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	if got := string(registry.Servers[0].Scope); got != "public" {
		t.Fatalf("Scope = %q, want public", got)
	}
}

func TestServerScopeRejectsUnknownValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	doc := "version: v1\nservers:\n  - name: demo\n    image: demo\n    imageTag: v1\n    scope: internet\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromFile(path); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("LoadFromFile() error = %v, want an invalid scope error", err)
	}
}

func TestServerScopeWireFormat(t *testing.T) {
	server := ServerMetadata{Name: "demo", Scope: "org"}
	y, err := yaml.Marshal(server)
	if err != nil || !strings.Contains(string(y), "scope: org") {
		t.Fatalf("yaml = %q, %v; want scope: org", y, err)
	}
	j, err := json.Marshal(server)
	if err != nil || !strings.Contains(string(j), `"scope":"org"`) {
		t.Fatalf("json = %s, %v; want scope org", j, err)
	}
	empty, _ := json.Marshal(ServerMetadata{Name: "demo"})
	if strings.Contains(string(empty), "scope") {
		t.Fatalf("empty scope must be omitted, got %s", empty)
	}
}
