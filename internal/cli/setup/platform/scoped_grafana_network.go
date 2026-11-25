package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/assetpath"
)

func scopedTelemetryPolicySource() (*networkingv1.NetworkPolicy, error) {
	filename, err := assetpath.ResolveRepoAssetPath("k8s/23-scoped-observability-networkpolicy.yaml")
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- filename is resolved solely from the fixed release asset path above, never request input.
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal([]byte(strings.Split(string(data), "\n---\n")[0]), &policy); err != nil {
		return nil, err
	}
	if policy.Kind != "NetworkPolicy" || policy.Name != "mcp-scoped-observability-ingress" {
		return nil, fmt.Errorf("scoped telemetry ingress policy source is invalid")
	}
	return &policy, nil
}

func verifyScopedTelemetryPolicy() error {
	want, err := scopedTelemetryPolicySource()
	if err != nil {
		return err
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, err := clients.Clientset.NetworkingV1().NetworkPolicies(core.ComponentNamespace("grafana")).Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("scoped telemetry policy must be applied before Grafana auth proxy: %w", err)
	}
	if !reflect.DeepEqual(live.Spec, want.Spec) {
		return fmt.Errorf("live scoped telemetry ingress policy differs from this release; Grafana auth proxy remains disabled")
	}
	return nil
}

// Do not infer enforcement from the existence of NetworkPolicy resources or
// Kubernetes distribution. In particular, kindnet accepts policies but does not
// enforce them. Only an explicit supported provider is enabled: a fully ready
// Cilium with Kubernetes NetworkPolicy turned on, or k3s's embedded network
// policy controller proven by every node's recorded start arguments. Unknown
// providers stay on the existing admin-only path without an auth proxy.
func scopedGrafanaPolicyCapability() (bool, string) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return false, "cannot verify NetworkPolicy enforcement"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if supported, reason := networkPolicyCapability(ctx, clients.Clientset); !supported {
		return false, reason
	}
	if err := rejectAdditionalTelemetryPolicies(ctx, clients.Clientset); err != nil {
		return false, err.Error()
	}
	if !ciliumInstalled(ctx, clients.Clientset) {
		return true, "k3s embedded network policy controller enforces Kubernetes NetworkPolicy"
	}
	// Native Cilium rules are additive to Kubernetes rules too. This initial
	// supported mode refuses existing custom rules instead of guessing how
	// arbitrary Cilium selectors, entities, or L7 extensions affect the boundary.
	if clients.Dynamic == nil {
		return false, "cannot inspect additive Cilium policies"
	}
	for _, resource := range []string{"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies"} {
		gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: resource}
		list, err := clients.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, "cannot verify additive Cilium policies"
		}
		if err == nil && len(list.Items) > 0 {
			return false, "existing native Cilium policies require a supported isolation review before scoped Grafana can be enabled"
		}
	}
	return true, "ready Cilium explicitly enforces Kubernetes NetworkPolicy"
}

// networkPolicyCapability reports whether the cluster positively enforces
// Kubernetes NetworkPolicy. kindnet is refused first. When Cilium is installed
// its own rules decide, and k3s evidence never rescues an unready Cilium.
func networkPolicyCapability(ctx context.Context, cs kubernetes.Interface) (bool, string) {
	if cs == nil {
		return false, "cannot verify NetworkPolicy enforcement"
	}
	if ciliumInstalled(ctx, cs) {
		return ciliumPolicyCapability(ctx, cs)
	}
	kindnet, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "kindnet", metav1.GetOptions{})
	if err == nil && kindnet.Status.DesiredNumberScheduled > 0 {
		return false, "Kind kindnet does not enforce NetworkPolicy"
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "cannot inspect the cluster network provider"
	}
	return k3sPolicyCapability(ctx, cs)
}

func ciliumInstalled(ctx context.Context, cs kubernetes.Interface) bool {
	_, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
	return err == nil
}

// k3sPolicyCapability accepts k3s only on positive evidence. k3s enforces
// NetworkPolicy with an embedded controller that has no object to inspect, but
// it records each node's start arguments in the k3s.io/node-args annotation.
// Every node must carry it, none may disable the controller, and none may
// replace flannel with another CNI (whose policy behavior is unknown here).
// Settings made some other way are invisible, so this proves intent, not
// traffic: run the network boundary test on the cluster before relying on it.
func k3sPolicyCapability(ctx context.Context, cs kubernetes.Interface) (bool, string) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, "cannot inspect the cluster nodes"
	}
	if len(nodes.Items) == 0 {
		return false, "network provider is not a supported verified NetworkPolicy provider"
	}
	for _, node := range nodes.Items {
		raw, ok := node.Annotations["k3s.io/node-args"]
		if !ok {
			return false, "node " + node.Name + " is not a k3s node, so NetworkPolicy enforcement cannot be verified"
		}
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return false, "cannot read k3s start arguments on node " + node.Name
		}
		for i, arg := range args {
			name, value, hasValue := strings.Cut(arg, "=")
			switch name {
			case "--disable-network-policy":
				if !hasValue || !strings.EqualFold(strings.TrimSpace(value), "false") {
					return false, "k3s node " + node.Name + " disables the embedded network policy controller"
				}
			case "--flannel-backend":
				if !hasValue && i+1 < len(args) {
					value = args[i+1]
				}
				if strings.EqualFold(strings.TrimSpace(value), "none") {
					return false, "k3s node " + node.Name + " replaces flannel with another CNI, whose NetworkPolicy behavior is not verified"
				}
			}
		}
	}
	return true, "k3s embedded network policy controller is enabled on every node"
}

func rejectAdditionalTelemetryPolicies(ctx context.Context, cs kubernetes.Interface) error {
	policies, err := cs.NetworkingV1().NetworkPolicies(core.ComponentNamespace("grafana")).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify additive telemetry ingress policies")
	}
	for _, policy := range policies.Items {
		if policy.Name == "mcp-scoped-observability-ingress" || len(policy.Spec.Ingress) == 0 {
			continue
		}
		// Dropping non-app requirements is conservative: they might be added
		// to a pod by admission. An explicit app exclusion is enough to prove
		// this policy cannot select any managed telemetry backend.
		selector := metav1.LabelSelector{MatchLabels: map[string]string{}}
		if app := policy.Spec.PodSelector.MatchLabels["app"]; app != "" {
			selector.MatchLabels["app"] = app
		}
		for _, expr := range policy.Spec.PodSelector.MatchExpressions {
			if expr.Key == "app" {
				selector.MatchExpressions = append(selector.MatchExpressions, expr)
			}
		}
		parsed, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil {
			return fmt.Errorf("cannot verify ingress selector for %s", policy.Name)
		}
		for _, app := range []string{"grafana", "prometheus", "loki", "tempo"} {
			if parsed.Matches(labels.Set{"app": app}) {
				return fmt.Errorf("additional ingress policy %s may widen telemetry access", policy.Name)
			}
		}
	}
	return nil
}

func ciliumPolicyCapability(ctx context.Context, cs kubernetes.Interface) (bool, string) {
	if cs == nil {
		return false, "cannot verify NetworkPolicy enforcement"
	}
	kindnet, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "kindnet", metav1.GetOptions{})
	if err == nil && kindnet.Status.DesiredNumberScheduled > 0 {
		return false, "Kind kindnet does not enforce NetworkPolicy"
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "cannot inspect the cluster network provider"
	}
	config, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
	if err != nil {
		return false, "network provider is not the supported verified Cilium configuration"
	}
	mode := strings.TrimSpace(config.Data["enable-policy"])
	if mode != "default" && mode != "always" || config.Data["enable-k8s-networkpolicy"] != "true" {
		return false, "Cilium must explicitly enable Kubernetes NetworkPolicy with enable-policy=default or always"
	}
	agents, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
	if err != nil || agents.Status.DesiredNumberScheduled < 1 || agents.Status.NumberReady != agents.Status.DesiredNumberScheduled ||
		agents.Status.UpdatedNumberScheduled != agents.Status.DesiredNumberScheduled || agents.Status.NumberAvailable != agents.Status.DesiredNumberScheduled || agents.Status.ObservedGeneration < agents.Generation {
		return false, "Cilium agents are not all current, ready and available"
	}
	found := false
	for _, container := range agents.Spec.Template.Spec.Containers {
		if container.Name != "cilium-agent" {
			continue
		}
		found = true
		if len(container.Command) != 1 || container.Command[0] != "cilium-agent" {
			return false, "Cilium agent must run its supported explicit command"
		}
		policyFlag, k8sFlag := false, false
		args := append(append([]string{}, container.Command...), container.Args...)
		for i, arg := range args {
			if strings.HasPrefix(arg, "--enable-policy=") && arg != "--enable-policy=default" && arg != "--enable-policy=always" || strings.HasPrefix(arg, "--enable-k8s-networkpolicy=") && arg != "--enable-k8s-networkpolicy=true" {
				return false, "Cilium agent arguments override policy enforcement"
			}
			if arg == "--enable-policy" && (i+1 >= len(args) || args[i+1] != "default" && args[i+1] != "always") || arg == "--enable-k8s-networkpolicy" && (i+1 >= len(args) || args[i+1] != "true") {
				return false, "Cilium agent arguments override policy enforcement"
			}
			policyFlag = policyFlag || arg == "--enable-policy=default" || arg == "--enable-policy=always" || arg == "--enable-policy" && i+1 < len(args) && (args[i+1] == "default" || args[i+1] == "always")
			k8sFlag = k8sFlag || arg == "--enable-k8s-networkpolicy=true" || arg == "--enable-k8s-networkpolicy" && i+1 < len(args) && args[i+1] == "true"
		}
		// Current/ready agents with these explicit bound flags establish running
		// policy intent independently of stale or node-overridden ConfigMaps.
		if !policyFlag || !k8sFlag {
			return false, "Cilium agents must explicitly set --enable-policy=default|always and --enable-k8s-networkpolicy=true"
		}
		for _, env := range container.Env {
			if env.Name == "CILIUM_ENABLE_POLICY" && env.Value != "default" && env.Value != "always" || env.Name == "CILIUM_ENABLE_K8S_NETWORKPOLICY" && env.Value != "true" {
				return false, "Cilium agent environment overrides policy enforcement"
			}
		}
	}
	if !found {
		return false, "supported Cilium agent container was not found"
	}
	return true, "ready Cilium explicitly enforces Kubernetes NetworkPolicy"
}

func renderGrafanaDeploymentAccess(content string, scoped bool) string {
	if !scoped {
		return content // canonical manifest disables proxy auth and tenant Explore
	}
	for _, name := range []string{"GF_AUTH_PROXY_ENABLED", "GF_AUTH_DISABLE_LOGIN_FORM", "GF_AUTH_DISABLE_SIGNOUT_MENU", "GF_USERS_VIEWERS_CAN_EDIT"} {
		content = strings.ReplaceAll(content, "- name: "+name+"\n              value: \"false\"", "- name: "+name+"\n              value: \"true\"")
	}
	return content
}

func renderGrafanaIngressAccess(content string, scoped bool) (string, error) {
	docs := strings.Split(content, "\n---\n")
	for i, doc := range docs {
		var header struct {
			Kind     string
			Metadata struct{ Name string }
		}
		if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
			return "", fmt.Errorf("decode Grafana access manifest: %w", err)
		}
		if header.Kind != "Ingress" || header.Metadata.Name != "mcp-platform-gateway-observability" && header.Metadata.Name != "mcp-platform-observability" {
			continue
		}
		var ingress networkingv1.Ingress
		if err := yaml.Unmarshal([]byte(doc), &ingress); err != nil {
			return "", err
		}
		if scoped {
			ingress.Namespace = core.ComponentNamespace("ui")
			delete(ingress.Annotations, "traefik.ingress.kubernetes.io/router.middlewares")
		} else {
			ingress.Namespace = core.ComponentNamespace("grafana")
			if ingress.Annotations == nil {
				ingress.Annotations = map[string]string{}
			}
			ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = "platform-admin-auth@file"
		}
		for j := range ingress.Spec.Rules {
			if ingress.Spec.Rules[j].HTTP == nil {
				continue
			}
			for k := range ingress.Spec.Rules[j].HTTP.Paths {
				backend := &ingress.Spec.Rules[j].HTTP.Paths[k].Backend
				if scoped {
					backend.Service = &networkingv1.IngressServiceBackend{Name: "mcp-ui", Port: networkingv1.ServiceBackendPort{Number: 8082}}
				} else {
					backend.Service = &networkingv1.IngressServiceBackend{Name: "grafana", Port: networkingv1.ServiceBackendPort{Number: 3000}}
				}
			}
		}
		data, err := yaml.Marshal(ingress)
		if err != nil {
			return "", err
		}
		docs[i] = strings.TrimSuffix(string(data), "\n")
	}
	return strings.Join(docs, "\n---\n"), nil
}
