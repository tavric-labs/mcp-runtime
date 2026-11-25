package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupplan "mcp-runtime/internal/cli/setup/plan"
)

func TestRenderPodEgressCIDRs(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_CIDRS", "10.0.0.0/8")
	t.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", "10.42.0.0/16,10.43.0.0/16")
	content := "" +
		"            - name: MCP_POD_EGRESS_CIDRS\n" +
		"              value: \"\" # MCP_POD_EGRESS_CIDRS\n" +
		"            - name: MCP_POD_EGRESS_EXCEPT_CIDRS\n" +
		"              value: \"\" # MCP_POD_EGRESS_EXCEPT_CIDRS\n"
	rendered, err := renderAnalyticsManifest(content, AnalyticsImageSet{}, "", setupplan.PlatformModeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, `value: "10.0.0.0/8" # MCP_POD_EGRESS_CIDRS`) {
		t.Fatalf("destination CIDR was not rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, `value: "10.42.0.0/16,10.43.0.0/16" # MCP_POD_EGRESS_EXCEPT_CIDRS`) {
		t.Fatalf("exception CIDRs were not rendered:\n%s", rendered)
	}
}

func TestRenderPodEgressCIDRsRejectsDefaultRoute(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_CIDRS", "0.0.0.0/0")
	t.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", "")
	_, err := renderAnalyticsManifest("value: \"\" # MCP_POD_EGRESS_CIDRS\n", AnalyticsImageSet{}, "", setupplan.PlatformModeOrg)
	if err == nil {
		t.Fatal("default route was accepted")
	}
}

const allowAllPlaceholder = "            - name: MCP_POD_EGRESS_ALLOW_ALL\n" +
	"              value: \"\" # MCP_POD_EGRESS_ALLOW_ALL\n"

// Test mode is a throwaway cluster: setup lets servers reach any destination by
// turning the runtime API's allow-all egress switch on. A normal install never does.
func TestPodEgressAllowAllIsOnlySetInTestMode(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_CIDRS", "")
	t.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", "")

	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	rendered, err := applyPodEgressManifestValues(allowAllPlaceholder)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, `value: "true" # MCP_POD_EGRESS_ALLOW_ALL`) {
		t.Fatalf("test mode did not enable allow-all egress:\n%s", rendered)
	}

	for _, mode := range []string{"", "0"} {
		t.Setenv("MCP_RUNTIME_TEST_MODE", mode)
		rendered, err := applyPodEgressManifestValues(allowAllPlaceholder)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rendered, `value: "" # MCP_POD_EGRESS_ALLOW_ALL`) {
			t.Fatalf("MCP_RUNTIME_TEST_MODE=%q must leave allow-all egress off:\n%s", mode, rendered)
		}
	}
}

func TestRuntimeAPIManifestDeclaresTheAllowAllEgressSwitch(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "k8s", "08-runtime-api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `value: "" # MCP_POD_EGRESS_ALLOW_ALL`) {
		t.Fatal("k8s/08-runtime-api.yaml has no MCP_POD_EGRESS_ALLOW_ALL placeholder, so setup cannot turn the switch on")
	}
}
