package regression

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// retiredBaseNamespace is the legacy base MCP server namespace. Servers live in
// catalog namespaces (mcp-servers-org) and team namespaces; nothing in the
// platform may create, default to, grant access in, or probe the old name.
const retiredBaseNamespace = "mcp-servers"

// retiredNamespaceAllowed lists the only places that may still name it: the
// changelog and the live-platform retirement runbook, which must refer to the
// old objects in order to remove them.
var retiredNamespaceAllowed = map[string]bool{
	"CHANGELOG.md": true,
	"hack/deploy/reference/base-namespace-retirement.md": true,
	"test/regression/base_namespace_retired_test.go":     true,
}

func TestBaseNamespaceIsRetired(t *testing.T) {
	root := repoRoot(t)
	skipDirs := map[string]bool{".git": true, "node_modules": true, "graphify-out": true, "bin": true, ".gocache": true, ".gomodcache": true, ".cache": true, "static": true}
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if retiredNamespaceAllowed[rel] || !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 4<<20 || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if mentionsRetiredNamespace(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the %q base namespace is retired; use the org catalog namespace, a team namespace or the platform namespace instead.\n%s", retiredBaseNamespace, strings.Join(offenders, "\n"))
	}
}

// mentionsRetiredNamespace matches the bare name only, not mcp-servers-org,
// mcp-servers-public or longer identifiers that merely contain it.
func mentionsRetiredNamespace(line string) bool {
	for start := 0; ; {
		i := strings.Index(line[start:], retiredBaseNamespace)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(retiredBaseNamespace)
		before, after := byte(' '), byte(' ')
		if i > 0 {
			before = line[i-1]
			// Shell defaults like ${VAR:-mcp-servers} put a hyphen before the
			// name that is an operator, not part of a longer identifier.
			if before == '-' && i > 1 && line[i-2] == ':' {
				before = ' '
			}
		}
		if end < len(line) {
			after = line[end]
		}
		if !isNameByte(before) && !isNameByte(after) {
			return true
		}
		start = end
	}
}

func isNameByte(c byte) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func TestMentionsRetiredNamespace(t *testing.T) {
	for line, want := range map[string]bool{
		"namespace: mcp-servers":                   true,
		"-n mcp-servers get pods":                  true,
		`"mcp-servers"`:                            true,
		"mcp-servers/fixture":                      true,
		"mcp-servers-org":                          false,
		"mcp-servers-public,mcp-servers-org":       false,
		"x-mcp-servers":                            false,
		"cully.mcp-servers.svc":                    true,
		"mcp-servers-org and then mcp-servers too": true,
	} {
		if got := mentionsRetiredNamespace(line); got != want {
			t.Errorf("%q: got %v want %v", line, got, want)
		}
	}
}
