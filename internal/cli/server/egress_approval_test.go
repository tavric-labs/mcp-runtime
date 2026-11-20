package server

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/access"
)

const egressServerJSON = `{"metadata":{"name":"youtube","namespace":"mcp-servers-org","resourceVersion":"42","annotations":{}},"spec":{"egress":{"hosts":["www.googleapis.com"],"ports":[443]}}}`

func egressApprovalManager(getJSON string) (*ServerManager, *core.MockExecutor) {
	exec := &core.MockExecutor{DefaultOutput: []byte(getJSON)}
	mgr := NewServerManager(core.NewTestKubectlClient(exec), zap.NewNop())
	mgr.useKube = true
	return mgr, exec
}

func patchCommand(t *testing.T, exec *core.MockExecutor) map[string]any {
	t.Helper()
	for _, c := range exec.Commands {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			for i, a := range c.Args {
				if a == "--patch" && i+1 < len(c.Args) {
					var doc map[string]any
					if err := json.Unmarshal([]byte(c.Args[i+1]), &doc); err != nil {
						t.Fatalf("patch is not JSON: %v", err)
					}
					return doc
				}
			}
		}
	}
	t.Fatalf("no kubectl patch was issued: %+v", exec.Commands)
	return nil
}

func TestApproveServerEgressPatchesAnnotationBoundToResourceVersion(t *testing.T) {
	mgr, exec := egressApprovalManager(egressServerJSON)
	if err := mgr.SetServerEgressApproval("youtube", "mcp-servers-org", true); err != nil {
		t.Fatalf("SetServerEgressApproval(approve) error = %v", err)
	}
	meta := patchCommand(t, exec)["metadata"].(map[string]any)
	if meta["resourceVersion"] != "42" {
		t.Fatalf("patch must pin the reviewed resourceVersion so a changed declaration cannot be approved, got %v", meta["resourceVersion"])
	}
	annotations := meta["annotations"].(map[string]any)
	if annotations[access.EgressApprovedAnnotation] != "true" {
		t.Fatalf("annotation = %v, want \"true\"", annotations[access.EgressApprovedAnnotation])
	}
}

func TestRevokeServerEgressRemovesAnnotation(t *testing.T) {
	mgr, exec := egressApprovalManager(egressServerJSON)
	if err := mgr.SetServerEgressApproval("youtube", "mcp-servers-org", false); err != nil {
		t.Fatalf("SetServerEgressApproval(revoke) error = %v", err)
	}
	annotations := patchCommand(t, exec)["metadata"].(map[string]any)["annotations"].(map[string]any)
	value, present := annotations[access.EgressApprovedAnnotation]
	if !present || value != nil {
		t.Fatalf("revoke must null the annotation in a merge patch, got present=%v value=%v", present, value)
	}
}

func TestApproveServerEgressRefusesServerWithoutDeclaration(t *testing.T) {
	mgr, exec := egressApprovalManager(`{"metadata":{"name":"youtube","resourceVersion":"7"},"spec":{}}`)
	err := mgr.SetServerEgressApproval("youtube", "mcp-servers-org", true)
	if err == nil || !strings.Contains(err.Error(), "declares no spec.egress") {
		t.Fatalf("error = %v, want a refusal because spec.egress is empty", err)
	}
	for _, c := range exec.Commands {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			t.Fatal("must not patch a server that declares no egress")
		}
	}
}

func TestApproveServerEgressRequiresUseKube(t *testing.T) {
	mgr, exec := egressApprovalManager(egressServerJSON)
	mgr.useKube = false
	err := mgr.SetServerEgressApproval("youtube", "mcp-servers-org", true)
	if err == nil || !strings.Contains(err.Error(), "--use-kube") {
		t.Fatalf("error = %v, want it to require --use-kube", err)
	}
	if len(exec.Commands) != 0 {
		t.Fatalf("no kubectl command may run without --use-kube, got %+v", exec.Commands)
	}
}
