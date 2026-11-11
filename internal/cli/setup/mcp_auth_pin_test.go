package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/zap"

	mcpauthpin "mcp-runtime/config/release/mcp-auth"
	"mcp-runtime/internal/cli/core"
)

// The pin file, not setup.go, owns the default MCP Auth version so a Dependabot
// bump of that one file is a complete change.
func TestSetupDefaultMCPAuthImageIsThePinFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "release", "mcp-auth", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := mcpauthpin.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	flag := New(core.NewRuntime(zap.NewNop()), nil).Flags().Lookup("mcp-auth-server-image")
	if flag == nil {
		t.Fatal("setup has no --mcp-auth-server-image flag")
	}
	if flag.DefValue != want {
		t.Fatalf("--mcp-auth-server-image default = %q, want the pinned %q", flag.DefValue, want)
	}
}

// Docs and profiles must point at the pin file instead of repeating the
// version, or every bump PR would need hand edits.
func TestMCPAuthVersionIsNotRepeatedOutsideThePin(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	re := regexp.MustCompile(`mcp-auth-server:\d+\.\d+\.\d+`)
	for _, dir := range []string{"docs", "config/deployments", "hack"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, ".png") {
				return err
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if m := re.Find(raw); m != nil {
				t.Errorf("%s repeats %q; reference config/release/mcp-auth/Dockerfile instead", path, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
