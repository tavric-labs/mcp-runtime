package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/pkg/access"
)

// SetServerEgressApproval approves or revokes a server's declared external
// egress by setting or removing mcpruntime.org/egress-approved.
//
// There is no platform API endpoint for this yet, so it is a direct Kubernetes
// operation for platform administrators and requires --use-kube. The approval
// is bound to what the administrator just reviewed: the patch pins the
// MCPServer's resourceVersion, so a publisher who changes spec.egress between
// the review and the approval makes the patch conflict instead of approving
// the new declaration.
func (m *ServerManager) SetServerEgressApproval(name, namespace string, approve bool) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	// #nosec G204 -- name/namespace validated via validateServerInput.
	raw, err := m.kubectl.Output([]string{"get", "mcpserver", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return core.WrapWithBaseAndContext(
			core.ErrGetMCPServerFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to get server %q in namespace %q", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
	}
	var current struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Egress *struct {
				Hosts []string `json:"hosts"`
				Ports []int32  `json:"ports"`
			} `json:"egress"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return core.WrapWithBase(nil, err, fmt.Sprintf("failed to parse server %q: %v", name, err))
	}
	if approve && (current.Spec.Egress == nil || len(current.Spec.Egress.Hosts) == 0) {
		return core.NewWithBase(nil, fmt.Sprintf("server %q declares no spec.egress, so there is nothing to approve", name))
	}
	if current.Metadata.ResourceVersion == "" {
		return core.NewWithBase(nil, fmt.Sprintf("server %q has no resourceVersion; refusing an unpinned egress change", name))
	}

	var annotation any // nil removes the annotation in a merge patch
	if approve {
		annotation = "true"
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"resourceVersion": current.Metadata.ResourceVersion,
		"annotations":     map[string]any{access.EgressApprovedAnnotation: annotation},
	}})
	if err != nil {
		return core.WrapWithBase(nil, err, fmt.Sprintf("failed to encode egress approval: %v", err))
	}
	args := []string{"patch", "mcpserver", name, "-n", namespace, "--type", "merge", "--patch", string(patch)}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		wrapped := core.WrapWithBaseAndContext(
			nil,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to update egress approval for server %q in namespace %q (a conflict means the server changed after review; re-run)", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to update egress approval")
		core.LogStructuredError(m.logger, wrapped, "Failed to update egress approval")
		return wrapped
	}
	if approve {
		core.Success(fmt.Sprintf("Approved egress for %s/%s: %s", namespace, name, strings.Join(current.Spec.Egress.Hosts, ", ")))
	} else {
		core.Success(fmt.Sprintf("Revoked egress approval for %s/%s", namespace, name))
	}
	return nil
}

func newEgressCmd(mgr *ServerManager) *cobra.Command {
	var namespace string
	egressCmd := &cobra.Command{
		Use:   "egress",
		Short: "Approve or revoke a server's external egress",
		Long: `Servers run under a default-deny egress policy. A server declares the external
hosts it needs with egress.hosts in its .mcp metadata, and a platform
administrator approves that declaration here. Until it is approved the server
reports ExternalEgressReady=False (PendingApproval) and keeps no external access.

Changing the declaration on redeploy removes the approval. These commands are
direct Kubernetes operations and require --use-kube with admin cluster access.`,
	}
	approveCmd := &cobra.Command{
		Use:   "approve [name]",
		Short: "Approve the declared external egress of an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.SetServerEgressApproval(args[0], namespace, true)
		},
	}
	revokeCmd := &cobra.Command{
		Use:   "revoke [name]",
		Short: "Revoke the external egress approval of an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.SetServerEgressApproval(args[0], namespace, false)
		},
	}
	egressCmd.PersistentFlags().StringVar(&namespace, "namespace", core.DefaultServerNamespace, "Namespace")
	egressCmd.AddCommand(approveCmd, revokeCmd)
	return egressCmd
}
