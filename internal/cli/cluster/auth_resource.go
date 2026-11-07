package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"mcp-runtime/pkg/authgateway"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"
)

const (
	// connectorSecretName is the Secret setup creates from the connectors'
	// client_secret_env variables. mcp-auth reads it with envFrom.
	connectorSecretName = "mcp-auth-connector-secrets" // #nosec G101 -- Secret object name, not a credential value
	// resourceWiringHashAnnotation restarts mcp-auth when the wiring changes.
	resourceWiringHashAnnotation = "mcpruntime.org/resource-wiring-hash"
)

type resourceInstall struct {
	spec   *authgateway.ResourceSpec
	dryRun bool
}

type resourceReport struct {
	changed bool
	hash    string
}

func newMCPAuthResourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "Wire an OAuth MCP server's own sign-in and resource client into MCP Auth",
		Long: "Wire an MCP server that signs people in through its own MCP Auth login method and " +
			"exchanges their token for the provider's. The command adds the resource's login method, " +
			"its resource client (a public key) and its connector to the installed MCP Auth, keeping " +
			"every entry it does not own. Deploy the MCP server first so MCP Auth already serves its resource.",
	}
	var file, kubeconfig, kubeContext string
	var yes, dryRun bool
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Add or update one resource's login, client and connector",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" || kubeconfig == "" || kubeContext == "" {
				return errors.New("--file, --kubeconfig, and --context are required")
			}
			if !dryRun && !yes {
				return errors.New("pass --yes to update MCP Auth, or --dry-run to preview")
			}
			data, err := os.ReadFile(file) // #nosec G304 -- explicit operator-supplied file, validated before use.
			if err != nil {
				return fmt.Errorf("read resource file: %w", err)
			}
			spec, err := parseResourceFile(data)
			if err != nil {
				return err
			}
			rules := clientcmd.NewDefaultClientConfigLoadingRules()
			rules.ExplicitPath = kubeconfig
			rest, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
			if err != nil {
				return fmt.Errorf("load selected Kubernetes context: %w", err)
			}
			client, err := kubernetes.NewForConfig(rest)
			if err != nil {
				return fmt.Errorf("connect to Kubernetes: %w", err)
			}
			report, err := applyResource(cmd.Context(), client, resourceInstall{spec: spec, dryRun: dryRun})
			if err != nil {
				return err
			}
			action := "unchanged"
			switch {
			case report.changed && dryRun:
				action = "would update"
			case report.changed:
				action = "updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s the MCP Auth wiring for resource %s in context %s\n", action, spec.ID, kubeContext)
			return nil
		},
	}
	apply.Flags().StringVar(&file, "file", "", "Resource file, YAML or JSON (no secrets: the connector names its client secret variable)")
	apply.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	apply.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and show whether MCP Auth would change, without writing")
	apply.Flags().BoolVar(&yes, "yes", false, "Apply the change without an interactive prompt")
	cmd.AddCommand(apply)
	return cmd
}

// newMCPAuthCmd groups the commands that manage the installed MCP Auth server's
// wiring: its main login connector and the OAuth resources that sign in on their own.
func newMCPAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp-auth",
		Short: "Manage the installed MCP Auth server's connectors and resources",
	}
	cmd.AddCommand(newMCPAuthConnectorCmd())
	cmd.AddCommand(newMCPAuthResourceCmd())
	return cmd
}

// parseResourceFile reads a resource file as YAML or JSON. The file sits beside the
// server's YAML metadata; JSON is valid YAML, so one conversion accepts both and the
// strict validation in authgateway.ParseResourceSpec applies to either. The
// conversion is here, not in pkg/authgateway, because that support package may not
// import Kubernetes libraries.
func parseResourceFile(data []byte) (*authgateway.ResourceSpec, error) {
	converted, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse resource file: %w", err)
	}
	return authgateway.ParseResourceSpec(converted)
}

// applyResource validates the installation, computes the wiring and, unless
// dry-run, writes it: the shared wiring ConfigMap, the connectors, then the
// MCP Auth Deployment. It never reads a Secret value.
func applyResource(ctx context.Context, c kubernetes.Interface, in resourceInstall) (*resourceReport, error) {
	wiring := in.spec.Wiring()
	auth, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read the installed MCP Auth: %w", err)
	}
	if len(auth.Spec.Template.Spec.Containers) == 0 {
		return nil, errors.New("the installed MCP Auth has no container")
	}
	env := plainEnv(auth.Spec.Template.Spec.Containers[0].Env)
	if env["MCP_AUTH_CONNECTOR"] == "" || env["MCP_AUTH_LOGIN_METHODS_FILE"] != "" {
		return nil, errors.New("the installed MCP Auth uses login methods mode; add this resource's login to its login methods file instead")
	}
	served := false
	for _, resource := range strings.Split(env["MCP_AUTH_RESOURCES"], ",") {
		served = served || resource == in.spec.ResourceURL
	}
	if !served {
		return nil, fmt.Errorf("MCP Auth does not serve %s yet; deploy the MCP server first so the operator registers its resource", in.spec.ResourceURL)
	}
	for name, want := range map[string]string{
		"MCP_AUTH_RESOURCE_LOGINS_FILE":  authgateway.ResourceLoginsFile,
		"MCP_AUTH_RESOURCE_CLIENTS_FILE": authgateway.ResourceClientsFile,
	} {
		if v := env[name]; v != "" && v != want {
			return nil, fmt.Errorf("the installed MCP Auth already sets %s to another file; merge this resource's entries into it by hand", name)
		}
	}
	if err := requireConnectorSecretKey(ctx, c, wiring.Connector); err != nil {
		return nil, err
	}

	connectors, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read the MCP Auth connectors: %w", err)
	}
	mergedConnectors, err := authgateway.MergeResourceConnector(connectors.Data["connectors.json"], wiring.ConnectorName, wiring.Connector)
	if err != nil {
		return nil, err
	}

	old, getErr := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	exists := getErr == nil
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, getErr
	}
	if exists && !ownedByRuntime(old.ObjectMeta) {
		return nil, errors.New("the MCP Auth wiring ConfigMap exists without Runtime ownership")
	}
	files := map[string]string{}
	if exists {
		for name, content := range old.Data {
			files[name] = content
		}
	}
	if files["resource-logins.json"], err = authgateway.MergeResourceLogins(files["resource-logins.json"], wiring.Method); err != nil {
		return nil, err
	}
	if files[authgateway.ResourceClientsKey], err = authgateway.MergeResourceClients(files[authgateway.ResourceClientsKey], wiring.Client); err != nil {
		return nil, err
	}
	hash := wiringHash(files, mergedConnectors)

	desired := auth.DeepCopy()
	mutateForResource(desired, hash)
	changed := !exists || !equalFiles(old.Data, files) ||
		connectors.Data["connectors.json"] != mergedConnectors ||
		!equality.Semantic.DeepEqual(auth.Spec, desired.Spec)
	report := &resourceReport{changed: changed, hash: hash}
	if in.dryRun || !changed {
		return report, nil
	}

	if exists {
		old.Data = files
		if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{}); err != nil {
			return nil, err
		}
	} else {
		cm := &corev1.ConfigMap{ObjectMeta: gatewayMeta(authgateway.WiringName), Data: files}
		if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return nil, err
		}
	}
	if connectors.Data["connectors.json"] != mergedConnectors {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			current.Data["connectors.json"] = mergedConnectors
			_, err = c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, current, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutateForResource(current, hash)
		_, err = c.AppsV1().Deployments(mcpAuthNamespace).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// mutateForResource points MCP Auth at the shared wiring files and marks the
// pod template so a change restarts it.
func mutateForResource(d *appsv1.Deployment, hash string) {
	container := &d.Spec.Template.Spec.Containers[0]
	container.Env = setPlainEnv(container.Env, "MCP_AUTH_RESOURCE_LOGINS_FILE", authgateway.ResourceLoginsFile)
	container.Env = setPlainEnv(container.Env, "MCP_AUTH_RESOURCE_CLIENTS_FILE", authgateway.ResourceClientsFile)
	ensureWiringMount(d)
	if d.Spec.Template.Annotations == nil {
		d.Spec.Template.Annotations = map[string]string{}
	}
	d.Spec.Template.Annotations[resourceWiringHashAnnotation] = hash
}

// requireConnectorSecretKey checks, by key name only, that the connector's
// client secret variable already exists in the connector Secret, so MCP Auth
// is not restarted into a failing login. It never reads a value.
func requireConnectorSecretKey(ctx context.Context, c kubernetes.Interface, connector json.RawMessage) error {
	var entry struct {
		SecretEnv string `json:"client_secret_env"`
	}
	if err := json.Unmarshal(connector, &entry); err != nil || entry.SecretEnv == "" {
		return errors.New("the connector has no client_secret_env")
	}
	secret, err := c.CoreV1().Secrets(mcpAuthNamespace).Get(ctx, connectorSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read the %s Secret: %w", connectorSecretName, err)
	}
	if _, ok := secret.Data[entry.SecretEnv]; !ok {
		return fmt.Errorf("the %s Secret has no %s key; add the connector's client secret there before applying", connectorSecretName, entry.SecretEnv)
	}
	return nil
}

func wiringHash(files map[string]string, connectors string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		sum.Write([]byte(name + "\x00" + files[name] + "\x00"))
	}
	sum.Write([]byte(connectors))
	return hex.EncodeToString(sum.Sum(nil))
}

func equalFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, content := range a {
		if other, ok := b[name]; !ok || other != content {
			return false
		}
	}
	return true
}
