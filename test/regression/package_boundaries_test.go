package regression

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Check compiled dependency graphs, including transitive imports and the
// gateway's separate module. A directory layout alone cannot enforce ownership.
func TestSharedInfrastructureDependencyBoundaries(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		name      string
		dir       string
		packages  []string
		allowed   map[string]bool
		forbidden []string
	}{
		{
			name:     "foundational helpers use only the standard library",
			dir:      root,
			packages: []string{"./pkg/envcfg", "./pkg/identity", "./pkg/httpx", "./pkg/transport/tlsconfig"},
			allowed: map[string]bool{
				"mcp-runtime/pkg/envcfg":              true,
				"mcp-runtime/pkg/httpx":               true,
				"mcp-runtime/pkg/identity":            true,
				"mcp-runtime/pkg/transport/tlsconfig": true,
			},
		},
		{
			name:     "HTTP authentication does not initialize service telemetry or Kafka",
			dir:      root,
			packages: []string{"./pkg/platformauth", "./pkg/transport/httptrace", "./pkg/apihttp"},
			forbidden: []string{
				"github.com/segmentio/kafka-go", "github.com/prometheus/",
				"go.opentelemetry.io/otel/exporters/", "go.opentelemetry.io/otel/sdk/",
				"k8s.io/", "sigs.k8s.io/",
			},
		},
		{
			name:     "policy does not depend on service infrastructure",
			dir:      root,
			packages: []string{"./pkg/policy"},
			forbidden: []string{
				"mcp-runtime/pkg/transport/", "mcp-runtime/pkg/telemetry",
				"mcp-runtime/pkg/envcfg", "k8s.io/", "sigs.k8s.io/",
			},
		},
		{
			name:      "gateway does not depend on Kubernetes clients or Kafka",
			dir:       filepath.Join(root, "services/mcp-gateway"),
			packages:  []string{"."},
			forbidden: []string{"github.com/segmentio/kafka-go", "k8s.io/", "sigs.k8s.io/"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}"}, tc.packages...)
			cmd := exec.CommandContext(t.Context(), "go", args...)
			cmd.Dir = tc.dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("resolve dependencies: %v\n%s", err, output)
			}
			for dependency := range strings.FieldsSeq(string(output)) {
				if tc.allowed != nil && !tc.allowed[dependency] {
					t.Errorf("unexpected dependency %s", dependency)
				}
				for _, prefix := range tc.forbidden {
					if strings.HasPrefix(dependency, prefix) {
						t.Errorf("forbidden dependency %s", dependency)
					}
				}
			}
		})
	}
}
