package teamintentworker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/kubeworkload"
)

// ErrInvalidConfig indicates an unrecoverable team intent or reconciler
// configuration error (such as an empty or reserved platform namespace).
var ErrInvalidConfig = errors.New("invalid team intent configuration")

const (
	teamSlugLabel           = "mcpruntime.org/team-slug"
	teamScopeLabel          = "mcpruntime.org/scope"
	podSecurityEnforceLabel = "pod-security.kubernetes.io/enforce"
	podSecurityAuditLabel   = "pod-security.kubernetes.io/audit"
	podSecurityWarnLabel    = "pod-security.kubernetes.io/warn"
)

// K8sNamespaceReconciler provisions and cleans up Kubernetes team namespace
// prerequisites for durable team intents using ownership-checked operations.
type K8sNamespaceReconciler struct {
	Client                   kubernetes.Interface
	EnsureRegistryPullSecret func(context.Context, kubernetes.Interface, string) error
	Traefik                  k8sprovision.TraefikWatchConfig
	IngressFromNamespaces    []string
}

// ReconcileActive ensures the owned team namespace, its security/quota/RBAC
// prerequisites, registry pull secret, and Traefik watch entry exist.
func (r *K8sNamespaceReconciler) ReconcileActive(ctx context.Context, intent apikit.TeamNamespaceIntent) (string, error) {
	if r == nil || r.Client == nil {
		return "", fmt.Errorf("%w: kubernetes client is required", ErrInvalidConfig)
	}
	namespace := strings.TrimSpace(intent.Namespace)
	teamID := strings.TrimSpace(intent.TeamID)
	if namespace == "" || teamID == "" {
		return "", fmt.Errorf("%w: namespace and team ID are required", ErrInvalidConfig)
	}
	if kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return "", fmt.Errorf("%w: namespace %q is reserved for platform infrastructure", ErrInvalidConfig, namespace)
	}

	labels := map[string]string{
		kubeworkload.PlatformManagedLabel: "true",
		k8sprovision.TeamIDLabel:          teamID,
		teamSlugLabel:                     strings.TrimSpace(intent.Slug),
		teamScopeLabel:                    "team",
		podSecurityEnforceLabel:           "restricted",
		podSecurityAuditLabel:             "restricted",
		podSecurityWarnLabel:              "restricted",
	}

	provisioner := k8sprovision.Provisioner{
		Client:                   r.Client,
		EnsureRegistryPullSecret: r.EnsureRegistryPullSecret,
	}
	uid, err := provisioner.EnsureOwnedTeamNamespace(
		ctx,
		namespace,
		teamID,
		intent.PriorDeletedTeamIDs,
		labels,
		r.ingressAllowNamespaces()...,
	)
	if err != nil {
		return "", err
	}
	if r.Traefik.Mode != "disabled" {
		if err := k8sprovision.EnsureTeamTraefikWatch(ctx, r.Client, namespace, r.Traefik); err != nil {
			return "", err
		}
	}
	return uid, nil
}

// CleanupDeleted implements NamespaceReconciler by removing the Traefik watch
// for a deleted team incarnation while retaining its namespace.
func (r *K8sNamespaceReconciler) CleanupDeleted(ctx context.Context, intent apikit.TeamNamespaceIntent) error {
	return r.CleanupDeletedTeamTraefikWatch(ctx, intent.Namespace, intent.TeamID)
}

// CleanupDeletedTeamTraefikWatch inspects the target namespace's ownership
// label before touching Traefik. If the namespace is owned by a different team
// incarnation or is unmanaged, it returns ErrNamespaceOwnershipConflict without
// removing the Traefik watch. The namespace itself is always retained.
func (r *K8sNamespaceReconciler) CleanupDeletedTeamTraefikWatch(ctx context.Context, namespace, teamID string) error {
	if r == nil || r.Client == nil {
		return fmt.Errorf("%w: kubernetes client is required", ErrInvalidConfig)
	}
	namespace = strings.TrimSpace(namespace)
	teamID = strings.TrimSpace(teamID)
	if namespace == "" || teamID == "" {
		return fmt.Errorf("%w: namespace and team ID are required", ErrInvalidConfig)
	}
	if kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return fmt.Errorf("%w: namespace %q is reserved for platform infrastructure", ErrInvalidConfig, namespace)
	}

	ns, err := r.Client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if strings.TrimSpace(ns.Labels[kubeworkload.PlatformManagedLabel]) != "true" {
			return fmt.Errorf("%w: namespace %q is not platform-managed", k8sprovision.ErrNamespaceOwnershipConflict, namespace)
		}
		owner := strings.TrimSpace(ns.Labels[k8sprovision.TeamIDLabel])
		if owner != "" && owner != teamID {
			return fmt.Errorf("%w: namespace %q is owned by team %q, not %q", k8sprovision.ErrNamespaceOwnershipConflict, namespace, owner, teamID)
		}
	}

	return k8sprovision.RemoveTraefikDeploymentWatchesNamespace(ctx, r.Client, namespace, r.Traefik)
}

func (r *K8sNamespaceReconciler) ingressAllowNamespaces() []string {
	if len(r.IngressFromNamespaces) > 0 {
		return r.IngressFromNamespaces
	}
	if r.Traefik.Mode == "disabled" {
		return []string{"kube-system"}
	}
	if ns := strings.TrimSpace(r.Traefik.Namespace); ns != "" {
		return []string{ns}
	}
	return nil
}
