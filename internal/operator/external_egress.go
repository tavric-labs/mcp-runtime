package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/access"
)

// External egress (issue #957).
//
// MCP server pods run under a default-deny egress NetworkPolicy, so a server
// that must call an external API (for example Google for a YouTube server)
// needs an explicit, reviewed opening. A publisher declares the hosts in
// spec.egress; the operator applies it only after a platform administrator
// approves it with the EgressApprovedAnnotation.
//
// An approved declaration becomes one NetworkPolicy owned by the MCPServer
// that allows the declared TCP ports to public addresses only. Kubernetes
// NetworkPolicy cannot match hostnames, so the declared hosts are recorded for
// review and audit rather than enforced by name: the policy excludes private,
// loopback, link-local (including the cloud metadata address) and shared
// address ranges so it cannot be used to reach cluster-internal services.
const (
	// EgressApprovedAnnotation marks a server's spec.egress as approved by a
	// platform administrator. Only the exact value "true" approves.
	EgressApprovedAnnotation = access.EgressApprovedAnnotation

	// ExternalEgressReadyCondition reports the state of the declared egress.
	ExternalEgressReadyCondition = "ExternalEgressReady"

	ExternalEgressReasonApplied         = "Applied"
	ExternalEgressReasonPendingApproval = "PendingApproval"
	ExternalEgressReasonNotRequested    = "NotRequested"
	ExternalEgressReasonInvalid         = "InvalidEgress"

	externalEgressPolicySuffix = "-external-egress"
	dnsSubdomainMaxLength      = 253
)

// externalEgressExcludedCIDRs are never reachable through the external egress
// policy: RFC 1918, loopback, link-local (cloud metadata) and carrier-grade NAT
// space, which includes common pod and service ranges such as k3s 10.42/10.43.
var externalEgressExcludedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"100.64.0.0/10",
}

func externalEgressPolicyName(server string) string {
	name := server + externalEgressPolicySuffix
	if len(name) <= dnsSubdomainMaxLength {
		return name
	}
	sum := sha256.Sum256([]byte(server))
	suffix := "-" + hex.EncodeToString(sum[:])[:8] + externalEgressPolicySuffix
	return strings.TrimRight(server[:dnsSubdomainMaxLength-len(suffix)], "-.") + suffix
}

func externalEgressApproved(server *mcpv1alpha1.MCPServer) bool {
	return server.Annotations[EgressApprovedAnnotation] == "true"
}

// validateExternalEgress checks a spec.egress declaration and returns the
// normalized, sorted ports.
func validateExternalEgress(egress *mcpv1alpha1.EgressConfig) ([]int32, error) {
	if err := access.ValidateServerEgress(egress.Hosts, egress.Ports); err != nil {
		return nil, fmt.Errorf("spec.%w", err)
	}
	ports := slices.Clone(egress.Ports)
	if len(ports) == 0 {
		ports = []int32{access.DefaultServerEgressPort}
	}
	slices.Sort(ports)
	return slices.Compact(ports), nil
}

// reconcileExternalEgress keeps the owned external egress policy aligned with
// spec.egress and its approval. Invalid or unapproved declarations never leave
// a policy behind.
func (r *MCPServerReconciler) reconcileExternalEgress(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	key := types.NamespacedName{Namespace: mcpServer.Namespace, Name: externalEgressPolicyName(mcpServer.Name)}
	egress := mcpServer.Spec.Egress
	if egress == nil {
		if err := r.deleteExternalEgressPolicy(ctx, mcpServer, key); err != nil {
			return err
		}
		// Only report on servers that previously declared egress, so plain
		// servers keep a clean status.
		if meta.FindStatusCondition(mcpServer.Status.Conditions, ExternalEgressReadyCondition) != nil {
			r.setExternalEgressCondition(ctx, mcpServer, true, ExternalEgressReasonNotRequested, "spec.egress is not set; the server uses the default egress policy")
		}
		return nil
	}

	ports, err := validateExternalEgress(egress)
	if err != nil {
		if delErr := r.deleteExternalEgressPolicy(ctx, mcpServer, key); delErr != nil {
			return delErr
		}
		r.setExternalEgressCondition(ctx, mcpServer, false, ExternalEgressReasonInvalid, err.Error())
		return nil
	}
	if !externalEgressApproved(mcpServer) {
		if err := r.deleteExternalEgressPolicy(ctx, mcpServer, key); err != nil {
			return err
		}
		r.setExternalEgressCondition(ctx, mcpServer, false, ExternalEgressReasonPendingApproval, fmt.Sprintf(
			"external egress to %s is waiting for a platform administrator to approve it (annotation %s=true); the server stays under the default egress policy",
			strings.Join(egress.Hosts, ", "), EgressApprovedAnnotation))
		return nil
	}

	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	tcp := corev1.ProtocolTCP
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		policy.Labels = map[string]string{
			LabelManagedBy: LabelManagedByValue,
			LabelApp:       mcpServer.Name,
			labelComponent: externalEgressComponent,
		}
		policyPorts := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
		for _, port := range ports {
			target := intstr.FromInt32(port)
			policyPorts = append(policyPorts, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &target})
		}
		policy.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelApp: mcpServer.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: slices.Clone(externalEgressExcludedCIDRs)},
				}},
				Ports: policyPorts,
			}},
		}
		return ctrl.SetControllerReference(mcpServer, policy, r.Scheme)
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("External egress NetworkPolicy reconciled", "operation", op, "name", policy.Name, "namespace", policy.Namespace, "ports", ports)
	}
	r.setExternalEgressCondition(ctx, mcpServer, true, ExternalEgressReasonApplied, fmt.Sprintf(
		"NetworkPolicy %s allows TCP %v to public addresses for declared hosts %s",
		policy.Name, ports, strings.Join(egress.Hosts, ", ")))
	return nil
}

const externalEgressComponent = "external-egress"

// deleteExternalEgressPolicy removes the policy only when this server
// controls it, so a same-named policy created by someone else is left alone.
func (r *MCPServerReconciler) deleteExternalEgressPolicy(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, key types.NamespacedName) error {
	policy := &networkingv1.NetworkPolicy{}
	if err := r.Get(ctx, key, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !metav1.IsControlledBy(policy, mcpServer) {
		return nil
	}
	if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// setExternalEgressCondition writes ExternalEgressReady when it changes and
// emits a Warning event when it turns False. Failures are logged, not returned:
// the condition is diagnostic and must not block reconciliation.
func (r *MCPServerReconciler) setExternalEgressCondition(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, ready bool, reason, message string) {
	logger := log.FromContext(ctx)
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	latest := &mcpv1alpha1.MCPServer{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpServer), latest); err != nil {
		logger.V(1).Info("Skipping ExternalEgressReady condition update", "error", err.Error())
		return
	}
	existing := meta.FindStatusCondition(latest.Status.Conditions, ExternalEgressReadyCondition)
	if existing != nil && existing.Status == status && existing.Reason == reason && existing.Message == message && existing.ObservedGeneration == latest.Generation {
		return
	}
	if !ready && r.Recorder != nil && (existing == nil || existing.Status != status || existing.Reason != reason) {
		r.Recorder.Eventf(latest, nil, corev1.EventTypeWarning, reason, "ReconcileExternalEgress", "%s", message)
	}
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type:               ExternalEgressReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: latest.Generation,
	})
	if err := r.Status().Update(ctx, latest); err != nil {
		logger.V(1).Info("ExternalEgressReady condition update failed; retrying on next reconcile", "error", err.Error())
	}
}
