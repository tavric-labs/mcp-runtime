package platform

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func k3sNode(name string, args ...string) *corev1.Node {
	raw, _ := json.Marshal(args)
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"k3s.io/node-args": string(raw)}}}
}

// k3s runs its own network policy controller with no DaemonSet to inspect, so
// the only positive evidence is each node's recorded start arguments.
func TestNetworkPolicyCapabilityK3s(t *testing.T) {
	plain := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
	badJSON := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "bad", Annotations: map[string]string{"k3s.io/node-args": "not-json"}}}
	config, agents := supportedCiliumFixtures()
	notReady := agents.DeepCopy()
	notReady.Status.NumberReady = 0

	for _, tc := range []struct {
		name    string
		objects []runtime.Object
		want    bool
	}{
		{"default k3s server", []runtime.Object{k3sNode("a", "server", "--node-name", "a", "--tls-san", "x")}, true},
		{"server and agents", []runtime.Object{k3sNode("a", "server"), k3sNode("b", "agent", "--server", "https://a:6443")}, true},
		{"explicitly disabled policy controller", []runtime.Object{k3sNode("a", "server", "--disable-network-policy")}, false},
		{"disabled with a value", []runtime.Object{k3sNode("a", "server", "--disable-network-policy=true")}, false},
		{"disable flag set false is still enabled", []runtime.Object{k3sNode("a", "server", "--disable-network-policy=false")}, true},
		{"one node disables it", []runtime.Object{k3sNode("a", "server"), k3sNode("b", "agent", "--disable-network-policy")}, false},
		{"flannel replaced by another CNI", []runtime.Object{k3sNode("a", "server", "--flannel-backend=none")}, false},
		{"flannel replaced, separate value", []runtime.Object{k3sNode("a", "server", "--flannel-backend", "none")}, false},
		{"no nodes", nil, false},
		{"not a k3s node", []runtime.Object{plain}, false},
		{"one node is not k3s", []runtime.Object{k3sNode("a", "server"), plain}, false},
		{"unreadable node args", []runtime.Object{badJSON}, false},
		{"empty node args", []runtime.Object{k3sNode("a")}, true},
		{"kindnet is never enforced", []runtime.Object{k3sNode("a", "server"), &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "kindnet", Namespace: "kube-system"}, Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1}}}, false},
		{"cilium present: its own rules decide, ready", []runtime.Object{k3sNode("a", "server"), config, agents}, true},
		{"cilium present: not ready is not rescued by k3s", []runtime.Object{k3sNode("a", "server"), config, notReady}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(tc.objects...)
			supported, reason := networkPolicyCapability(context.Background(), cs)
			if supported != tc.want || reason == "" {
				t.Fatalf("supported=%v reason=%q, want %v", supported, reason, tc.want)
			}
			for _, action := range cs.Actions() {
				if verb := action.GetVerb(); verb != "get" && verb != "list" {
					t.Fatalf("capability verification mutated the cluster: %v", action)
				}
			}
		})
	}
}
