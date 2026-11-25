package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"mcp-runtime/pkg/authgateway"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

const (
	mcpAuthNamespace     = "mcp-platform"
	mcpAuthConnectorName = "mcp-auth-connectors"
)

func newMCPAuthConnectorCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "connector", Short: "Manage the bundled MCP Auth identity connector"}
	var file, connector, kubeconfig, kubeContext string
	var yes, dryRun bool
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Update the existing MCP Auth connector configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" || connector == "" || kubeconfig == "" || kubeContext == "" {
				return errors.New("--file, --connector, --kubeconfig, and --context are required")
			}
			if !dryRun && !yes {
				return errors.New("pass --yes to update the connector ConfigMap")
			}
			data, err := os.ReadFile(file) // #nosec G304 -- explicit operator-supplied file, validated before use.
			if err != nil {
				return fmt.Errorf("read connector file: %w", err)
			}
			if err := validateMCPAuthConnector(data, connector); err != nil {
				return err
			}
			rules := clientcmd.NewDefaultClientConfigLoadingRules()
			rules.ExplicitPath = kubeconfig
			cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext})
			rest, err := cfg.ClientConfig()
			if err != nil {
				return fmt.Errorf("load selected Kubernetes context: %w", err)
			}
			client, err := kubernetes.NewForConfig(rest)
			if err != nil {
				return fmt.Errorf("connect to Kubernetes: %w", err)
			}
			changed, err := applyMCPAuthConnector(cmd.Context(), client, string(data), dryRun)
			if err != nil {
				return err
			}
			action := "unchanged"
			if changed && dryRun {
				action = "would update"
			} else if changed {
				action = "updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s/%s in context %s\n", action, mcpAuthNamespace, mcpAuthConnectorName, kubeContext)
			return nil
		},
	}
	apply.Flags().StringVar(&file, "file", "", "Connector JSON file without credentials")
	apply.Flags().StringVar(&connector, "connector", "", "Connector name used by the MCP Auth deployment")
	apply.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	apply.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "Show whether the connector ConfigMap would change")
	apply.Flags().BoolVar(&yes, "yes", false, "Apply the change without an interactive prompt")
	cmd.AddCommand(apply)
	return cmd
}

func validateMCPAuthConnector(data []byte, selected string) error {
	var connectors map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&connectors); err != nil || len(connectors) == 0 {
		return errors.New("connector file must be a nonempty JSON object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("connector file must contain exactly one JSON object")
	}
	raw, ok := connectors[selected]
	if !ok {
		return fmt.Errorf("connector %q is not present in the file", selected)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return fmt.Errorf("connector %q must be a nonempty JSON object", selected)
	}
	for name, raw := range connectors {
		var config map[string]json.RawMessage
		if err := json.Unmarshal(raw, &config); err != nil || len(config) == 0 {
			return fmt.Errorf("connector %q must be a nonempty JSON object", name)
		}
		if _, found := config["mcp_scopes"]; found {
			return fmt.Errorf("connector %q uses removed mcp_scopes; configure scopes on each MCP server", name)
		}
		if _, found := config["client_secret"]; found {
			return fmt.Errorf("connector %q contains a literal client_secret; use client_secret_env", name)
		}
		if secret, found := config["client_secret_env"]; found {
			var envName string
			if err := json.Unmarshal(secret, &envName); err != nil || strings.TrimSpace(envName) == "" {
				return fmt.Errorf("connector %q has an empty client_secret_env", name)
			}
		}
	}
	return nil
}

func applyMCPAuthConnector(ctx context.Context, client kubernetes.Interface, data string, dryRun bool) (bool, error) {
	var changed bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := client.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read existing MCP Auth connector ConfigMap: %w", err)
		}
		merged, err := authgateway.PreserveOwnedConnectors(cm.Data["connectors.json"], data)
		if err != nil {
			return err
		}
		if err := requireNoNewDanglingLogin(ctx, client, cm.Data["connectors.json"], merged); err != nil {
			return err
		}
		if cm.Data["connectors.json"] == merged {
			changed = false
			return nil
		}
		changed = true
		if dryRun {
			return nil
		}
		updated := cm.DeepCopy()
		if updated.Data == nil {
			updated.Data = make(map[string]string)
		}
		updated.Data["connectors.json"] = merged
		_, err = client.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
	return changed, err
}

// requireNoNewDanglingLogin refuses a connector apply that would leave a login
// method in the wiring ConfigMap naming a connector the new file does not define.
// A method that already named a missing connector does not block the command, so
// it can still be used to repair that state.
func requireNoNewDanglingLogin(ctx context.Context, client kubernetes.Interface, current, merged string) error {
	wiring, err := client.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the MCP Auth wiring ConfigMap: %w", err)
	}
	logins := wiring.Data["resource-logins.json"]
	before, err := authgateway.MissingLoginConnectors(logins, current)
	if err != nil {
		return err
	}
	already := map[authgateway.MissingLoginConnector]bool{}
	for _, m := range before {
		already[m] = true
	}
	after, err := authgateway.MissingLoginConnectors(logins, merged)
	if err != nil {
		return err
	}
	var fresh []authgateway.MissingLoginConnector
	for _, m := range after {
		if !already[m] {
			fresh = append(fresh, m)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	reasons := make([]string, len(fresh))
	for i, m := range fresh {
		reasons[i] = m.String()
	}
	return fmt.Errorf("refusing to apply, nothing was changed: %s; MCP Auth would exit at startup. Keep that connector in the file, or change the login method first", strings.Join(reasons, "; "))
}
