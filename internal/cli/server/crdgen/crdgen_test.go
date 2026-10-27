package crdgen

import (
	"mcp-runtime/pkg/topology"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mcp-runtime/pkg/blueprint"
)

// Environment names read by the registry-host helpers in pkg/blueprint.
const (
	envMCPRegistryPullHost    = "MCP_REGISTRY_PULL_HOST"
	envMCPRegistryEndpoint    = "MCP_REGISTRY_ENDPOINT"
	envMCPRegistryHost        = "MCP_REGISTRY_HOST"
	envMCPRegistryIngressHost = "MCP_REGISTRY_INGRESS_HOST"
	envMCPPlatformDomain      = "MCP_PLATFORM_DOMAIN"
)

func TestGenerateCRD(t *testing.T) {
	t.Run("generates valid CRD YAML", func(t *testing.T) {
		clearRegistryImageEnv(t)
		// Prevent registry host rewriting so we can test CRD field mapping in isolation.
		t.Setenv(envMCPRegistryPullHost, topology.DefaultRegistryHost)
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "test-server.yaml")

		replicas := int32(2)
		server := &blueprint.ServerMetadata{
			Name:             "test-server",
			Description:      "Test server for CRD generation.",
			Image:            "my-image",
			ImageTag:         "v1.0.0",
			Route:            "/test/mcp",
			IngressHost:      "example.local",
			PublicPathPrefix: "test-server",
			Port:             9000,
			Replicas:         &replicas,
			Namespace:        "custom-ns",
			Scope:            topology.ScopeOrg,
			TeamID:           "team-custom",
		}

		err := GenerateCRD(server, outputPath)
		if err != nil {
			t.Fatalf("GenerateCRD failed: %v", err)
		}

		// Verify file exists
		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}

		content := string(data)

		assertContains(t, content, "apiVersion: mcpruntime.org/v1alpha1")
		assertContains(t, content, "kind: MCPServer")
		assertContains(t, content, "name: test-server")
		assertContains(t, content, "namespace: custom-ns")
		assertContains(t, content, "mcpruntime.org/scope: org")
		assertContains(t, content, "teamID: team-custom")
		assertContains(t, content, "description: Test server for CRD generation.")
		assertContains(t, content, "image: my-image")
		assertContains(t, content, "imageTag: v1.0.0")
		assertContains(t, content, "port: 9000")
		assertContains(t, content, "replicas: 2")
		assertContains(t, content, "ingressPath: /test/mcp")
		assertContains(t, content, "ingressHost: example.local")
		assertContains(t, content, "publicPathPrefix: test-server")
	})

	t.Run("generates CRD with resources", func(t *testing.T) {
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "resource-server.yaml")

		server := &blueprint.ServerMetadata{
			Name:      "resource-server",
			Image:     "my-image",
			Namespace: "default",
			Resources: &blueprint.ResourceRequirements{
				Limits: &blueprint.ResourceList{
					CPU:    "500m",
					Memory: "512Mi",
				},
				Requests: &blueprint.ResourceList{
					CPU:    "100m",
					Memory: "128Mi",
				},
			},
		}

		err := GenerateCRD(server, outputPath)
		if err != nil {
			t.Fatalf("GenerateCRD failed: %v", err)
		}

		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}

		content := string(data)
		assertContains(t, content, "cpu: 500m")
		assertContains(t, content, "memory: 512Mi")
		assertContains(t, content, "cpu: 100m")
		assertContains(t, content, "memory: 128Mi")
	})

	t.Run("generates CRD with environment variables", func(t *testing.T) {
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "env-server.yaml")

		server := &blueprint.ServerMetadata{
			Name:      "env-server",
			Image:     "my-image",
			Namespace: "default",
			EnvVars: []blueprint.EnvVar{
				{Name: "DATABASE_URL", Value: "postgres://localhost"},
				{Name: "LOG_LEVEL", Value: "debug"},
			},
		}

		err := GenerateCRD(server, outputPath)
		if err != nil {
			t.Fatalf("GenerateCRD failed: %v", err)
		}

		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}

		content := string(data)
		assertContains(t, content, "name: DATABASE_URL")
		assertContains(t, content, "value: postgres://localhost")
		assertContains(t, content, "name: LOG_LEVEL")
		assertContains(t, content, "value: debug")
	})

	t.Run("generates CRD with gateway and analytics", func(t *testing.T) {
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "gateway-server.yaml")

		server := &blueprint.ServerMetadata{
			Name:      "gateway-server",
			Image:     "my-image",
			Namespace: "default",
			Gateway: &blueprint.GatewayConfig{
				Enabled:     boolPtr(true),
				Image:       "example.com/mcp-gateway:latest",
				Port:        8091,
				UpstreamURL: "http://127.0.0.1:8088",
				StripPrefix: "/gateway-server",
				Resources: &blueprint.ResourceRequirements{
					Requests: &blueprint.ResourceList{CPU: "5m", Memory: "32Mi"},
				},
			},
			Auth: &blueprint.AuthConfig{},
			Policy: &blueprint.PolicyConfig{
				Mode:            blueprint.PolicyMode("allow-list"),
				DefaultDecision: blueprint.PolicyDecision("deny"),
			},
			Session: &blueprint.SessionConfig{
				Required: true,
			},
			Tools: []blueprint.ToolConfig{
				{Name: "delete_user", Description: "Delete a user from the backing system.", RequiredTrust: blueprint.TrustLevel("high"), SideEffect: blueprint.ToolSideEffect("destructive")},
			},
			SecretEnvVars: []blueprint.SecretEnvVar{
				{
					Name: "OPENAI_API_KEY",
					SecretKeyRef: &blueprint.SecretKeyRef{
						Name: "provider-creds",
						Key:  "openai",
					},
				},
			},
			Analytics: &blueprint.AnalyticsConfig{
				IngestURL: "http://analytics.default.svc/api/events",
				Source:    "gateway-server",
				EventType: "mcp.request",
				APIKeySecretRef: &blueprint.SecretKeyRef{
					Name: "analytics-creds",
					Key:  "api-key",
				},
			},
			Rollout: &blueprint.RolloutConfig{
				Strategy:       blueprint.RolloutStrategy("Canary"),
				CanaryReplicas: int32Ptr(1),
			},
		}

		err := GenerateCRD(server, outputPath)
		if err != nil {
			t.Fatalf("GenerateCRD failed: %v", err)
		}

		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}

		var rendered map[string]any
		if err := yaml.Unmarshal(data, &rendered); err != nil {
			t.Fatalf("failed to unmarshal generated yaml: %v", err)
		}

		spec := assertMapValue(t, rendered, "spec")
		gateway := assertMapValue(t, spec, "gateway")
		assertMapBoolValue(t, gateway, "enabled", true)
		assertMapStringValue(t, gateway, "image", "example.com/mcp-gateway:latest")
		assertMapIntValue(t, gateway, "port", 8091)
		assertMapStringValue(t, gateway, "upstreamURL", "http://127.0.0.1:8088")
		assertMapStringValue(t, gateway, "stripPrefix", "/gateway-server")
		gatewayResources := assertMapValue(t, gateway, "resources")
		gatewayRequests := assertMapValue(t, gatewayResources, "requests")
		assertMapStringValue(t, gatewayRequests, "cpu", "5m")
		assertMapStringValue(t, gatewayRequests, "memory", "32Mi")

		auth := assertMapValue(t, spec, "auth")
		if _, exists := auth["mode"]; exists {
			t.Fatal("generated auth contains retired mode field")
		}

		policy := assertMapValue(t, spec, "policy")
		assertMapStringValue(t, policy, "mode", "allow-list")
		assertMapStringValue(t, policy, "defaultDecision", "deny")

		session := assertMapValue(t, spec, "session")
		assertMapBoolValue(t, session, "required", true)

		tools := assertSliceValue(t, spec, "tools")
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(tools))
		}
		tool := assertMapItem(t, tools[0], "tools[0]")
		assertMapStringValue(t, tool, "name", "delete_user")
		assertMapStringValue(t, tool, "description", "Delete a user from the backing system.")
		assertMapStringValue(t, tool, "requiredTrust", "high")
		assertMapStringValue(t, tool, "sideEffect", "destructive")

		secretEnvVars := assertSliceValue(t, spec, "secretEnvVars")
		if len(secretEnvVars) != 1 {
			t.Fatalf("expected 1 secret env var, got %d", len(secretEnvVars))
		}
		secretEnv := assertMapItem(t, secretEnvVars[0], "secretEnvVars[0]")
		assertMapStringValue(t, secretEnv, "name", "OPENAI_API_KEY")
		secretKeyRef := assertMapValue(t, secretEnv, "secretKeyRef")
		assertMapStringValue(t, secretKeyRef, "name", "provider-creds")
		assertMapStringValue(t, secretKeyRef, "key", "openai")

		analytics := assertMapValue(t, spec, "analytics")
		// "disabled" is omitted from output when false (omitempty); analytics
		// is on by default whenever the operator has an ingest URL.
		assertMapStringValue(t, analytics, "ingestURL", "http://analytics.default.svc/api/events")
		assertMapStringValue(t, analytics, "source", "gateway-server")
		assertMapStringValue(t, analytics, "eventType", "mcp.request")
		apiKeySecretRef := assertMapValue(t, analytics, "apiKeySecretRef")
		assertMapStringValue(t, apiKeySecretRef, "name", "analytics-creds")
		assertMapStringValue(t, apiKeySecretRef, "key", "api-key")

		rollout := assertMapValue(t, spec, "rollout")
		assertMapStringValue(t, rollout, "strategy", "Canary")
		assertMapIntValue(t, rollout, "canaryReplicas", 1)
	})

	t.Run("creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "nested", "dirs", "server.yaml")

		server := &blueprint.ServerMetadata{
			Name:      "nested-server",
			Image:     "my-image",
			Namespace: "default",
		}

		err := GenerateCRD(server, outputPath)
		if err != nil {
			t.Fatalf("GenerateCRD failed: %v", err)
		}

		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("expected file to be created in nested directory")
		}
	})
}

func TestGenerateCRDsFromRegistry(t *testing.T) {
	t.Run("generates CRDs for all servers", func(t *testing.T) {
		clearRegistryImageEnv(t)
		// Prevent registry host rewriting so we can test CRD field mapping in isolation.
		t.Setenv(envMCPRegistryPullHost, topology.DefaultRegistryHost)
		tmpDir := t.TempDir()

		replicas := int32(1)
		registry := &blueprint.RegistryFile{
			Version: "v1",
			Servers: []blueprint.ServerMetadata{
				{
					Name:      "server-one",
					Image:     "image-one",
					Namespace: "ns1",
					Replicas:  &replicas,
				},
				{
					Name:      "server-two",
					Image:     "image-two",
					Namespace: "ns2",
					Replicas:  &replicas,
				},
			},
		}

		err := GenerateCRDsFromRegistry(registry, tmpDir)
		if err != nil {
			t.Fatalf("GenerateCRDsFromRegistry failed: %v", err)
		}

		// Verify both files exist
		for _, name := range []string{"server-one.yaml", "server-two.yaml"} {
			path := filepath.Join(tmpDir, name)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("expected file %s to exist", name)
			}
		}

		// Verify content of first file
		data, err := os.ReadFile(filepath.Join(tmpDir, "server-one.yaml"))
		if err != nil {
			t.Fatalf("failed to read server-one.yaml: %v", err)
		}
		assertContains(t, string(data), "name: server-one")
		assertContains(t, string(data), "image: image-one")
	})

	t.Run("creates output directory if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		outputDir := filepath.Join(tmpDir, "new-dir")

		registry := &blueprint.RegistryFile{
			Version: "v1",
			Servers: []blueprint.ServerMetadata{
				{Name: "test", Image: "test", Namespace: "default"},
			},
		}

		err := GenerateCRDsFromRegistry(registry, outputDir)
		if err != nil {
			t.Fatalf("GenerateCRDsFromRegistry failed: %v", err)
		}

		if _, err := os.Stat(outputDir); os.IsNotExist(err) {
			t.Error("expected output directory to be created")
		}
	})

	t.Run("handles empty registry", func(t *testing.T) {
		tmpDir := t.TempDir()

		registry := &blueprint.RegistryFile{
			Version: "v1",
			Servers: []blueprint.ServerMetadata{},
		}

		err := GenerateCRDsFromRegistry(registry, tmpDir)
		if err != nil {
			t.Fatalf("GenerateCRDsFromRegistry failed: %v", err)
		}

		// Verify no files created
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("failed to read directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty directory, got %d entries", len(entries))
		}
	})
}

func assertContains(t *testing.T, content, substr string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("expected content to contain %q, got:\n%s", substr, content)
	}
}

func assertMapValue(t *testing.T, data map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	return assertMapItem(t, value, key)
}

func assertSliceValue(t *testing.T, data map[string]any, key string) []any {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("expected key %q to be a slice, got %T", key, value)
	}
	return items
}

func assertMapItem(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	item, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected %s to be a map, got %T", label, value)
	}
	return item
}

func assertMapStringValue(t *testing.T, data map[string]any, key, want string) {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	got, ok := value.(string)
	if !ok {
		t.Fatalf("expected key %q to be a string, got %T", key, value)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func assertMapBoolValue(t *testing.T, data map[string]any, key string, want bool) {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	got, ok := value.(bool)
	if !ok {
		t.Fatalf("expected key %q to be a bool, got %T", key, value)
	}
	if got != want {
		t.Fatalf("%s = %t, want %t", key, got, want)
	}
}

func assertMapIntValue(t *testing.T, data map[string]any, key string, want int) {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	got, ok := value.(int)
	if !ok {
		t.Fatalf("expected key %q to be an int, got %T", key, value)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", key, got, want)
	}
}

func clearRegistryImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{envMCPRegistryPullHost, envMCPRegistryEndpoint, envMCPRegistryHost, envMCPRegistryIngressHost, envMCPPlatformDomain} {
		t.Setenv(k, "")
	}
}

func TestGenerateCRDRewritesImageWhenOnlyIngressHostSet(t *testing.T) {
	for _, k := range []string{envMCPRegistryPullHost, envMCPRegistryEndpoint, envMCPRegistryHost} {
		t.Setenv(k, "")
	}
	t.Setenv(envMCPRegistryIngressHost, "registry.mcpruntime.org")

	server := &blueprint.ServerMetadata{
		Name:      "acme-tools",
		Namespace: "mcp-team-acme",
		Image:     "registry.mcpruntime.org/acme/acme-tools",
		ImageTag:  "v0.1.0",
		Port:      8088,
	}
	tmp := t.TempDir()
	path := tmp + "/acme-tools.yaml"
	if err := GenerateCRD(server, path); err != nil {
		t.Fatalf("GenerateCRD: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated CRD: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "image: registry.registry.svc.cluster.local:5000/acme/acme-tools") {
		t.Fatalf("expected cluster pull host image rewrite, got:\n%s", content)
	}
	if strings.Contains(content, "image: registry.mcpruntime.org/acme/acme-tools") {
		t.Fatalf("image incorrectly kept public registry host:\n%s", content)
	}
}

func TestGenerateCRDRewritesImageToPullHost(t *testing.T) {
	t.Setenv(envMCPRegistryPullHost, "10.43.69.247:5000")
	t.Setenv(envMCPRegistryIngressHost, "registry.mcpruntime.org")

	server := &blueprint.ServerMetadata{
		Name:      "acme-tools",
		Namespace: "mcp-team-acme",
		Image:     "registry.mcpruntime.org/acme/acme-tools",
		ImageTag:  "v0.1.0",
		Port:      8088,
	}
	tmp := t.TempDir()
	path := tmp + "/acme-tools.yaml"
	if err := GenerateCRD(server, path); err != nil {
		t.Fatalf("GenerateCRD: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated CRD: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "image: 10.43.69.247:5000/acme/acme-tools") {
		t.Fatalf("expected pull-host image rewrite, got:\n%s", content)
	}
}

func boolPtr(v bool) *bool { return &v }

func int32Ptr(v int32) *int32 { return &v }
