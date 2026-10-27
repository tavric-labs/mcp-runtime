package regression

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEveryPackageDocumentsItsPurpose keeps pkg/ navigable: a shared package
// must say what it does, and what it deliberately does not, in its package doc.
func TestEveryPackageDocumentsItsPurpose(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.ImportPath}}\t{{.Doc}}", "./pkg/...")
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list packages: %v\n%s", err, output)
	}
	const minDocWords = 8
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		path, doc, _ := strings.Cut(line, "\t")
		if len(strings.Fields(doc)) < minDocWords {
			t.Errorf("%s: package doc %q is missing or too short; add a doc.go that says what the package does", path, doc)
		}
	}
}
