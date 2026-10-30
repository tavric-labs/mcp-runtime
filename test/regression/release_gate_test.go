package regression

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	Jobs map[string]releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Name     string   `yaml:"name"`
	Uses     string   `yaml:"uses"`
	Needs    []string `yaml:"needs"`
	If       string   `yaml:"if"`
	Strategy struct {
		Matrix map[string]any `yaml:"matrix"`
	} `yaml:"strategy"`
}

func loadReleaseWorkflow(t *testing.T, root, path string) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readFile(t, root, path)), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func releaseMatrix(t *testing.T, matrix map[string]any) []map[string]any {
	t.Helper()
	if includes, ok := matrix["include"].([]any); ok {
		var out []map[string]any
		for _, row := range includes {
			out = append(out, row.(map[string]any))
		}
		return out
	}
	keys := make([]string, 0, len(matrix))
	for key := range matrix {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := []map[string]any{{}}
	for _, key := range keys {
		values, ok := matrix[key].([]any)
		if !ok {
			t.Fatalf("unsupported release matrix axis %s", key)
		}
		var next []map[string]any
		for _, row := range rows {
			for _, value := range values {
				copy := map[string]any{key: value}
				for k, v := range row {
					copy[k] = v
				}
				next = append(next, copy)
			}
		}
		rows = next
	}
	return rows
}

// Resolve the workflow's real matrix job names, so adding/removing a cell cannot
// silently leave the publication verifier checking a different set of jobs.
func releaseJobNames(t *testing.T, root, path, prefix string) []string {
	t.Helper()
	workflow := loadReleaseWorkflow(t, root, path)
	expression := regexp.MustCompile(`\$\{\{\s*matrix\.([a-zA-Z0-9_-]+)\s*\}\}`)
	var names []string
	for id, job := range workflow.Jobs {
		for _, row := range releaseMatrix(t, job.Strategy.Matrix) {
			name := job.Name
			if name == "" {
				name = id
			}
			hasExpression := expression.MatchString(name)
			name = expression.ReplaceAllStringFunc(name, func(match string) string {
				key := expression.FindStringSubmatch(match)[1]
				return fmt.Sprint(row[key])
			})
			if !hasExpression && len(row) > 0 {
				keys := make([]string, 0, len(row))
				for key := range row {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				values := make([]string, 0, len(row))
				for _, key := range keys {
					values = append(values, fmt.Sprint(row[key]))
				}
				name += " (" + strings.Join(values, ", ") + ")"
			}
			name = prefix + name
			if strings.HasPrefix(job.Uses, "./.github/workflows/") {
				names = append(names, releaseJobNames(t, root, strings.TrimPrefix(job.Uses, "./"), name+" / ")...)
			} else {
				names = append(names, name)
			}
		}
	}
	return names
}

func TestReleaseGateCoversEveryWorkflowJobAndMatrixCell(t *testing.T) {
	root := repoRoot(t)
	workflow := loadReleaseWorkflow(t, root, ".github/workflows/pre-release-regression.yaml")
	gate := workflow.Jobs["release-gate"]
	if gate.If != "${{ always() }}" {
		t.Fatal("release gate must run even when a dependency fails or skips")
	}
	for id := range workflow.Jobs {
		if id != "release-gate" && !slices.Contains(gate.Needs, id) {
			t.Errorf("release gate omits dependency %s", id)
		}
	}
	command := exec.Command("python3", "-c", `import json,runpy; print(json.dumps(runpy.run_path('hack/release/verify_gate.py')['REQUIRED_JOBS']))`)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var required []string
	if err := json.Unmarshal(output, &required); err != nil {
		t.Fatal(err)
	}
	actual := releaseJobNames(t, root, filepath.Join(".github", "workflows", "pre-release-regression.yaml"), "")
	sort.Strings(actual)
	sort.Strings(required)
	if !slices.Equal(actual, required) {
		t.Fatalf("publication verifier disagrees with workflow cells\nactual: %v\nrequired: %v", actual, required)
	}
}
