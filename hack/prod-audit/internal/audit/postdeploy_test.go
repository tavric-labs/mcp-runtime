package audit

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig() PostDeployConfig {
	return PostDeployConfig{RuntimeCLI: "mcp-runtime", Kubeconfig: "explicit-config", KubeContext: "prod", ExpectedClusterUID: "expected", ReleaseTag: "v0.17.0", Manifest: "manifest.json", PlatformURL: "https://platform.mcpruntime.org", RegistryURL: "https://registry.mcpruntime.org", Issuer: "https://auth.mcpruntime.org/mcp-auth", PublicMCP: "https://mcp.mcpruntime.org/prod-audit/mcp", Team: "qa-audit"}
}

func TestPostDeployRejectsRealServicesAndCredentialRedirects(t *testing.T) {
	for _, target := range []string{"https://mcp.mcpruntime.org/cully/mcp", "https://mcp.mcpruntime.org/linkedin/mcp", "http://mcp.mcpruntime.org/prod-audit/mcp", "https://user:password@mcp.mcpruntime.org/prod-audit/mcp", "https://mcp.mcpruntime.org/prod-audit/mcp?token=bad"} {
		c := testConfig()
		c.PublicMCP = target
		if c.Validate() == nil {
			t.Fatalf("accepted unsafe target %s", target)
		}
	}
	c := testConfig()
	c.ClusterLogin = true
	c.PlatformURL = "https://unrelated.example.test"
	if c.Validate() == nil {
		t.Fatal("configured cluster credentials could be sent to a different origin")
	}
}

func TestClusterMismatchStopsBeforeLoginOrMutations(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"metadata\":{\"uid\":\"wrong-cluster\"}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := testConfig()
	c.ClusterLogin = true
	report, err := RunPostDeploy(context.Background(), c, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cluster identity") {
		t.Fatalf("wrong-cluster error=%v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != "failed" {
		t.Fatalf("continued after wrong cluster: %+v", report)
	}
	if report.Status != "failed" {
		t.Fatal("failed audit has no overall failure status")
	}
}

func TestReleaseInventoryRequiresAllPinnedImages(t *testing.T) {
	targets := []struct{ name, namespace, deployment, repository, tag string }{
		{"operator", "mcp-runtime", "mcp-runtime-operator-controller-manager", "mcp-runtime-operator", "v0.17.0-gsource"},
		{"platform-api", "mcp-platform", "mcp-platform-api", "mcp-platform-api", "v0.17.0-gsource"},
		{"runtime-api", "mcp-platform", "mcp-runtime-api", "mcp-runtime-api", "v0.17.0-gsource"},
		{"analytics-api", "mcp-observability", "mcp-analytics-api", "mcp-analytics-api", "v0.17.0-gsource"},
		{"ingest", "mcp-observability", "mcp-ingest", "mcp-ingest", "v0.17.0-gsource"},
		{"processor", "mcp-observability", "mcp-processor", "mcp-processor", "v0.17.0-gsource"},
		{"ui", "mcp-platform", "mcp-ui", "mcp-ui", "v0.17.0-gsource"},
		{"gateway", "mcp-platform", "mcp-platform-gateway", "traefik", "v3.7.14"},
	}
	items := []map[string]any{}
	deployment := func(ns, name, image string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": name, "namespace": ns, "generation": 2},
			"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []map[string]any{{"image": image}}}}},
			"status":   map[string]any{"observedGeneration": 2, "replicas": 1, "readyReplicas": 1, "availableReplicas": 1, "updatedReplicas": 1},
		}
	}
	components := []map[string]string{}
	for _, target := range targets {
		image := "registry.example.test/" + target.repository + ":" + target.tag
		if target.name == "gateway" {
			image = target.repository + ":" + target.tag
		}
		items = append(items, deployment(target.namespace, target.deployment, image))
		components = append(components, map[string]string{"name": target.name, "repository": target.repository, "tag": target.tag})
	}
	for _, ns := range []string{"registry", "cert-manager", "mcp-servers-org"} {
		items = append(items, deployment(ns, "fixture", "example.test/component:v1"))
	}
	manifest, _ := json.Marshal(map[string]any{"version": "v0.17.0", "components": components})
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	if err := checkProductionInventory(raw, manifestPath, "v0.17.0", "https://registry.example.test", ""); err != nil {
		t.Fatal(err)
	}
	items[0] = deployment(targets[0].namespace, targets[0].deployment, "registry.example.test/mcp-runtime-operator:old")
	raw, _ = json.Marshal(map[string]any{"items": items})
	if err := checkProductionInventory(raw, manifestPath, "v0.17.0", "https://registry.example.test", ""); err == nil {
		t.Fatal("old deployed image passed")
	}
	if err := checkProductionInventory(raw, manifestPath, "v0.16.0", "https://registry.example.test", ""); err == nil {
		t.Fatal("wrong release manifest passed")
	}
}

func TestPostDeployCommandDoesNotInterpretShellInput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "audit cli; literal")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "must-not-exist")
	argument := "$(touch " + marker + "); echo injected"
	r := &postRunner{env: os.Environ()}
	raw, err := r.command(context.Background(), script, argument)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != argument {
		t.Fatalf("argument was interpreted: %q", raw)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell input created a file: %v", err)
	}
}

func TestPostDeployCommandFailureRedactsChildOutput(t *testing.T) {
	script := filepath.Join(t.TempDir(), "audit-cli")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho synthetic-sensitive-output\necho synthetic-sensitive-error >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := &postRunner{env: os.Environ()}
	raw, err := r.command(context.Background(), script)
	if err == nil || raw != nil {
		t.Fatalf("expected a redacted command failure, got output %q and error %v", raw, err)
	}
	if strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("command failure exposed child output")
	}
}
