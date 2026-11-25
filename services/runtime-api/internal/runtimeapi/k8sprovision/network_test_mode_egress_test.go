package k8sprovision

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func allowsEveryDestination(rule networkingv1.NetworkPolicyEgressRule) bool {
	return len(rule.To) == 0 && len(rule.Ports) == 0
}

func ensuredPolicy(t *testing.T) *networkingv1.NetworkPolicy {
	t.Helper()
	client := fake.NewSimpleClientset()
	if err := EnsureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-servers-org"); err != nil {
		t.Fatal(err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-servers-org").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// MCP_POD_EGRESS_ALLOW_ALL is set by setup in --test-mode only. A throwaway
// cluster should let a server call any API and reach the in-cluster auth
// server, while a normal cluster keeps the default-deny egress untouched.
func TestDefaultDenyKeepsEgressRestrictedUnlessTheTestModeSwitchIsOn(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_ALLOW_ALL", "")
	for _, rule := range ensuredPolicy(t).Spec.Egress {
		if allowsEveryDestination(rule) {
			t.Fatal("the default policy allows every destination without the test-mode switch")
		}
	}
}

func TestTestModeSwitchAddsAnAllowAllEgressRuleAndKeepsTheRest(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_ALLOW_ALL", "")
	baseline := len(ensuredPolicy(t).Spec.Egress)

	t.Setenv("MCP_POD_EGRESS_ALLOW_ALL", "true")
	policy := ensuredPolicy(t)
	var open int
	for _, rule := range policy.Spec.Egress {
		if allowsEveryDestination(rule) {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("allow-all egress rules = %d, want exactly 1", open)
	}
	if len(policy.Spec.Egress) != baseline+1 {
		t.Fatalf("egress rules = %d, want the %d existing rules kept plus the one allow-all", len(policy.Spec.Egress), baseline)
	}
	hasEgressType := false
	for _, kind := range policy.Spec.PolicyTypes {
		hasEgressType = hasEgressType || kind == networkingv1.PolicyTypeEgress
	}
	if !hasEgressType {
		t.Fatal("the policy no longer declares Egress, so it would not be enforced")
	}
}

func TestOnlyTheValueTrueEnablesTheSwitch(t *testing.T) {
	for _, value := range []string{"false", "1", "yes", "TRUE ", "0"} {
		t.Setenv("MCP_POD_EGRESS_ALLOW_ALL", value)
		for _, rule := range ensuredPolicy(t).Spec.Egress {
			if allowsEveryDestination(rule) {
				t.Fatalf("value %q enabled allow-all egress; only the exact string \"true\" may", value)
			}
		}
	}
}
