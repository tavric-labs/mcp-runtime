package k8sprovision

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/pkg/kubeworkload"
)

const (
	// TeamIDLabel records the authoritative team incarnation ID that owns a
	// managed team namespace.
	TeamIDLabel = "mcpruntime.org/team-id"
)

var (
	// ErrNamespaceOwnershipConflict indicates an existing namespace is unmanaged
	// or owned by a team incarnation that the caller is not permitted to adopt.
	ErrNamespaceOwnershipConflict = errors.New("namespace ownership conflict")
	// ErrNamespaceTerminating indicates the target namespace is terminating and
	// provisioning must be retried after deletion finishes.
	ErrNamespaceTerminating = errors.New("namespace is terminating")
)

// Provisioner owns namespace defaults and the order of namespace-local access
// setup. The caller supplies credential issuance, which may use a remote broker
// or native registry without importing HTTP handlers into this package.
type Provisioner struct {
	Client                   kubernetes.Interface
	EnsureRegistryPullSecret func(context.Context, kubernetes.Interface, string) error
}

func (p Provisioner) EnsureNamespace(ctx context.Context, namespace string, labels map[string]string, ingressFromNamespaces ...string) error {
	if p.Client == nil || strings.TrimSpace(namespace) == "" {
		return nil
	}
	if p.EnsureRegistryPullSecret == nil {
		return fmt.Errorf("registry pull secret provisioner is required")
	}
	if kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return fmt.Errorf("managed MCPServer namespace %q is reserved for platform infrastructure", namespace)
	}
	base := p.Client
	current, err := base.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: labels,
		}}
		if _, err := base.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if err != nil {
		return err
	} else if mergeNamespaceLabels(current, labels) {
		if _, err := base.CoreV1().Namespaces().Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return p.ensureNamespaceResources(ctx, base, namespace, ingressFromNamespaces...)
}

// EnsureOwnedTeamNamespace creates or updates a managed team namespace only
// after proving ownership by teamID (or safe adoption from adoptablePriorTeamIDs).
// It refuses reserved, terminating, unmanaged, or differently-owned namespaces
// without mutating them, and returns the live namespace UID.
func (p Provisioner) EnsureOwnedTeamNamespace(ctx context.Context, namespace, teamID string, adoptablePriorTeamIDs []string, labels map[string]string, ingressFromNamespaces ...string) (string, error) {
	namespace = strings.TrimSpace(namespace)
	teamID = strings.TrimSpace(teamID)
	if p.Client == nil {
		return "", fmt.Errorf("kubernetes client is required")
	}
	if namespace == "" {
		return "", fmt.Errorf("namespace is required")
	}
	if teamID == "" {
		return "", fmt.Errorf("team ID is required")
	}
	if p.EnsureRegistryPullSecret == nil {
		return "", fmt.Errorf("registry pull secret provisioner is required")
	}
	if kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return "", fmt.Errorf("managed MCPServer namespace %q is reserved for platform infrastructure", namespace)
	}

	desiredLabels := make(map[string]string, len(labels)+2)
	for k, v := range labels {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			desiredLabels[trimmed] = v
		}
	}
	desiredLabels[kubeworkload.PlatformManagedLabel] = "true"
	desiredLabels[TeamIDLabel] = teamID

	base := p.Client
	var ns *corev1.Namespace
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, getErr := base.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			created, createErr := base.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   namespace,
					Labels: desiredLabels,
				},
			}, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(createErr) {
				// Retry via optimistic conflict path so ownership is validated against the existing object.
				return apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, namespace, createErr)
			}
			if createErr != nil {
				return createErr
			}
			ns = created
			return nil
		}
		if getErr != nil {
			return getErr
		}
		if current.Status.Phase == corev1.NamespaceTerminating || current.DeletionTimestamp != nil {
			return fmt.Errorf("%w: namespace %q is terminating", ErrNamespaceTerminating, namespace)
		}
		if strings.TrimSpace(current.Labels[kubeworkload.PlatformManagedLabel]) != "true" {
			return fmt.Errorf("%w: namespace %q is not platform-managed", ErrNamespaceOwnershipConflict, namespace)
		}
		currentOwner := strings.TrimSpace(current.Labels[TeamIDLabel])
		if currentOwner != teamID && !isAdoptablePriorTeamID(adoptablePriorTeamIDs, currentOwner) {
			return fmt.Errorf("%w: namespace %q is owned by team %q, not %q", ErrNamespaceOwnershipConflict, namespace, currentOwner, teamID)
		}
		updated := current.DeepCopy()
		if mergeNamespaceLabels(updated, desiredLabels) {
			updated.ResourceVersion = current.ResourceVersion
			saved, updateErr := base.CoreV1().Namespaces().Update(ctx, updated, metav1.UpdateOptions{})
			if updateErr != nil {
				return updateErr
			}
			ns = saved
			return nil
		}
		ns = current
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := p.ensureNamespaceResources(ctx, base, namespace, ingressFromNamespaces...); err != nil {
		return "", err
	}
	return string(ns.UID), nil
}

func isAdoptablePriorTeamID(adoptable []string, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, id := range adoptable {
		if strings.TrimSpace(id) == candidate {
			return true
		}
	}
	return false
}

func (p Provisioner) ensureNamespaceResources(ctx context.Context, base kubernetes.Interface, namespace string, ingressFromNamespaces ...string) error {
	if err := ensureResourceQuota(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureLimitRange(ctx, base, namespace); err != nil {
		return err
	}
	if err := EnsureDefaultDenyNetworkPolicy(ctx, base, namespace, ingressFromNamespaces...); err != nil {
		return err
	}
	if err := kubeworkload.EnsureServiceAccount(ctx, base, namespace); err != nil {
		return err
	}
	if err := EnsurePlatformAPISecretAccess(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureNamespaceOperatorSecretAccess(ctx, base, namespace); err != nil {
		return err
	}
	if err := p.EnsureRegistryPullSecret(ctx, base, namespace); err != nil {
		return fmt.Errorf("provision registry pull secret for namespace %q: %w", namespace, err)
	}
	return nil
}

// ensureNamespaceOperatorSecretAccess grants the operator Secret access only
// inside namespaces managed by the platform. The operator has no cluster-wide
// Secret permissions; this binding lets it reconcile server-local certificates
// and registry pull credentials without reaching unrelated platform Secrets.
func ensureNamespaceOperatorSecretAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	return kubeworkload.EnsureOperatorSecretAccess(ctx, client, namespace)
}

func mergeNamespaceLabels(ns *corev1.Namespace, labels map[string]string) bool {
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	changed := false
	for key, value := range labels {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if ns.Labels[key] == value {
			continue
		}
		ns.Labels[key] = value
		changed = true
	}
	return changed
}

func ensureResourceQuota(ctx context.Context, client kubernetes.Interface, ns string) error {
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-quota", Namespace: ns}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods:                   resource.MustParse("20"),
		corev1.ResourceRequestsCPU:            resource.MustParse("4"),
		corev1.ResourceRequestsMemory:         resource.MustParse("8Gi"),
		corev1.ResourceLimitsCPU:              resource.MustParse("8"),
		corev1.ResourceLimitsMemory:           resource.MustParse("16Gi"),
		corev1.ResourcePersistentVolumeClaims: resource.MustParse("4"),
	}}}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureLimitRange(ctx context.Context, client kubernetes.Interface, ns string) error {
	limit := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-limits", Namespace: ns}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Default: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}}}}
	if _, err := client.CoreV1().LimitRanges(ns).Create(ctx, limit, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
