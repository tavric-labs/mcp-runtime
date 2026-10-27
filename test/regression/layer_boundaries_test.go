package regression

import (
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
)

// layer orders the shared packages from most stable to most volatile. A package
// may import its own layer or a lower one, never a higher one:
//
//	composers (services, operator, CLI) > adapters > domain > leaves
//
// Support packages (HTTP, telemetry, service bootstrap) sit beside the domain:
// they may use leaves and domain code but never Kubernetes adapters.
type layer int

const (
	leaf layer = iota
	domain
	support
	adapter
)

func (l layer) String() string {
	return [...]string{"leaf", "domain", "support", "adapter"}[l]
}

// canImport reports whether a package in layer from may import one in layer to.
func canImport(from, to layer) bool {
	switch from {
	case leaf:
		return to == leaf
	case domain:
		return to == leaf || to == domain
	case support:
		return to == leaf || to == domain || to == support
	default:
		return to != support
	}
}

// pkgLayers classifies every package under pkg/. A new package must be added
// here, so its layer is a deliberate decision rather than an accident.
var pkgLayers = map[string]layer{
	// Leaves: no repository imports outside this layer.
	"authzmatrix": leaf, "credentialbroker": leaf, "egresscidrs": leaf,
	"envcfg": leaf, "errx": leaf, "events": leaf, "httpx": leaf,
	"identity": leaf, "manifest": leaf, "oidcdiscovery": leaf, "openapi": leaf,
	"platform": leaf, "platforminventory": leaf, "publicroutes": leaf,
	"publishscope": leaf, "registryauth": leaf, "runtimeconfig": leaf,
	"transport/httptrace": leaf, "transport/tlsconfig": leaf,

	// Domain: rules and models, no Kubernetes or service infrastructure.
	"access": domain, "access/specvalidation": domain, "metadata": domain,
	"platformmode": domain, "policy": domain, "sessionstate": domain,

	// Support: HTTP, telemetry and bootstrap shared by services.
	"apihttp": support, "authfile": support, "authgateway": support,
	"certauth": support, "clickhouse": support, "internalapi": support,
	"platformauth": support, "svcboot": support, "telemetry": support,
	"telemetry/kafkatrace": support,

	// Adapters: the only layer that talks to Kubernetes.
	"controlplane": adapter, "k8sclient": adapter, "kubeworkload": adapter,
	"operatorutil": adapter, "platformstack": adapter, "registrypush": adapter,
	"resourcehistory": adapter, "workloadidentity": adapter,
}

// k8sInDomain lists lower-layer packages that still import Kubernetes libraries
// directly. The list may only shrink: splitting each into a pure package and a
// Kubernetes store removes its entry, and an entry that no longer applies fails.
var k8sInDomain = map[string]string{
	"access":                "types and manager use Kubernetes objects; split into pure access and a k8s store",
	"access/specvalidation": "validates Kubernetes-typed specs; move with the access split",
	"metadata":              "loader uses metav1 and sigs.k8s.io/yaml; separate the manifest model from the loader",
}

type pkgInfo struct {
	imports []string // repository packages under pkg/, relative to pkg/
	k8s     bool     // imports k8s.io or sigs.k8s.io directly
}

func layerViolations(graph map[string]pkgInfo, layers map[string]layer, k8sAllowed map[string]string) []string {
	var problems []string
	for pkg, info := range graph {
		from, ok := layers[pkg]
		if !ok {
			problems = append(problems, "pkg/"+pkg+": not assigned to a layer in pkgLayers")
			continue
		}
		for _, imp := range info.imports {
			to, known := layers[imp]
			if !known {
				continue // reported on its own entry
			}
			if !canImport(from, to) {
				problems = append(problems, "pkg/"+pkg+" ("+from.String()+") imports pkg/"+imp+" ("+to.String()+")")
			}
		}
		_, allowed := k8sAllowed[pkg]
		switch {
		case info.k8s && from != adapter && !allowed:
			problems = append(problems, "pkg/"+pkg+" ("+from.String()+") imports Kubernetes libraries; only adapters may")
		case !info.k8s && allowed:
			problems = append(problems, "pkg/"+pkg+" no longer imports Kubernetes libraries; remove it from k8sInDomain")
		}
	}
	for pkg := range k8sAllowed {
		if _, ok := graph[pkg]; !ok {
			problems = append(problems, "pkg/"+pkg+" is in k8sInDomain but does not exist")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestLayerViolationsDetectsUpwardImportsAndKubernetesInLowerLayers(t *testing.T) {
	layers := map[string]layer{"id": leaf, "rules": domain, "store": adapter, "web": support}
	graph := map[string]pkgInfo{
		"id":    {imports: []string{"rules"}},         // leaf -> domain
		"rules": {imports: []string{"id"}, k8s: true}, // domain with k8s
		"store": {imports: []string{"rules"}, k8s: true},
		"web":   {imports: []string{"store"}}, // support -> adapter
		"new":   {},                           // unclassified
	}
	got := layerViolations(graph, layers, nil)
	want := []string{
		"pkg/id (leaf) imports pkg/rules (domain)",
		"pkg/new: not assigned to a layer in pkgLayers",
		"pkg/rules (domain) imports Kubernetes libraries; only adapters may",
		"pkg/web (support) imports pkg/store (adapter)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}

	allowed := map[string]string{"rules": "pending split", "gone": "stale"}
	got = layerViolations(map[string]pkgInfo{"rules": {k8s: false}}, map[string]layer{"rules": domain}, allowed)
	want = []string{
		"pkg/gone is in k8sInDomain but does not exist",
		"pkg/rules no longer imports Kubernetes libraries; remove it from k8sInDomain",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ratchet violations = %q, want %q", got, want)
	}
}

func TestSharedPackageLayerDesign(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.CommandContext(t.Context(), "go", "list", "-f",
		`{{.ImportPath}} {{range .Imports}}{{.}} {{end}}`, "./pkg/...")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list packages: %v\n%s", err, output)
	}
	const modulePkg = "mcp-runtime/pkg/"
	graph := map[string]pkgInfo{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		pkg := strings.TrimPrefix(fields[0], modulePkg)
		var info pkgInfo
		for _, imp := range fields[1:] {
			switch {
			case strings.HasPrefix(imp, modulePkg):
				info.imports = append(info.imports, strings.TrimPrefix(imp, modulePkg))
			case strings.HasPrefix(imp, "k8s.io/"), strings.HasPrefix(imp, "sigs.k8s.io/"):
				info.k8s = true
			}
		}
		graph[pkg] = info
	}
	for _, problem := range layerViolations(graph, pkgLayers, k8sInDomain) {
		t.Error(problem)
	}
}
