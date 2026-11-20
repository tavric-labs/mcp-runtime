package operator

import (
	"context"
	"slices"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func externalEgressServer(approved bool, hosts []string, ports []int32) *mcpv1alpha1.MCPServer {
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "youtube", Namespace: "mcp-servers-org", UID: "uid-1"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Egress: &mcpv1alpha1.EgressConfig{Hosts: hosts, Ports: ports},
		},
	}
	if approved {
		server.Annotations = map[string]string{EgressApprovedAnnotation: "true"}
	}
	return server
}

func externalEgressFixture(t *testing.T, server *mcpv1alpha1.MCPServer, objs ...client.Object) (*MCPServerReconciler, client.Client) {
	t.Helper()
	all := append([]client.Object{server}, objs...)
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithStatusSubresource(server).WithObjects(all...).Build()
	return &MCPServerReconciler{Client: c, Scheme: c.Scheme()}, c
}

func getExternalEgressPolicy(t *testing.T, c client.Client, server *mcpv1alpha1.MCPServer) (*networkingv1.NetworkPolicy, bool) {
	t.Helper()
	policy := &networkingv1.NetworkPolicy{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: server.Namespace, Name: externalEgressPolicyName(server.Name)}, policy)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return policy, true
}

func externalEgressCondition(t *testing.T, c client.Client, server *mcpv1alpha1.MCPServer) *metav1.Condition {
	t.Helper()
	latest := &mcpv1alpha1.MCPServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(server), latest); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(latest.Status.Conditions, ExternalEgressReadyCondition)
}

func TestExternalEgressApprovedCreatesOwnedPolicy(t *testing.T) {
	server := externalEgressServer(true, []string{"www.googleapis.com", "oauth2.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)

	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	policy, ok := getExternalEgressPolicy(t, c, server)
	if !ok {
		t.Fatal("approved egress did not create a NetworkPolicy")
	}
	if got := policy.Spec.PodSelector.MatchLabels[LabelApp]; got != "youtube" {
		t.Fatalf("pod selector app = %q, want youtube", got)
	}
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("policyTypes = %v, want [Egress]", policy.Spec.PolicyTypes)
	}
	if len(policy.OwnerReferences) != 1 || policy.OwnerReferences[0].Name != "youtube" || policy.OwnerReferences[0].Kind != "MCPServer" {
		t.Fatalf("owner references = %+v, want the MCPServer", policy.OwnerReferences)
	}
	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(policy.Spec.Egress))
	}
	rule := policy.Spec.Egress[0]
	if len(rule.Ports) != 1 || rule.Ports[0].Port == nil || rule.Ports[0].Port.IntVal != 443 {
		t.Fatalf("ports = %+v, want default TCP 443", rule.Ports)
	}
	if len(rule.To) != 1 || rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
		t.Fatalf("peers = %+v, want one ipBlock 0.0.0.0/0", rule.To)
	}
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"} {
		if !slices.Contains(rule.To[0].IPBlock.Except, want) {
			t.Errorf("except list %v is missing %s", rule.To[0].IPBlock.Except, want)
		}
	}
	cond := externalEgressCondition(t, c, server)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ExternalEgressReasonApplied {
		t.Fatalf("condition = %+v, want True/Applied", cond)
	}
}

func TestExternalEgressUsesDeclaredPortsSorted(t *testing.T) {
	server := externalEgressServer(true, []string{"api.example.com"}, []int32{8443, 443})
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	policy, _ := getExternalEgressPolicy(t, c, server)
	var got []int32
	for _, p := range policy.Spec.Egress[0].Ports {
		got = append(got, p.Port.IntVal)
	}
	if !slices.Equal(got, []int32{443, 8443}) {
		t.Fatalf("ports = %v, want [443 8443]", got)
	}
}

func TestExternalEgressPendingApprovalCreatesNoPolicy(t *testing.T) {
	server := externalEgressServer(false, []string{"www.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)

	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if _, ok := getExternalEgressPolicy(t, c, server); ok {
		t.Fatal("unapproved egress must not create a NetworkPolicy")
	}
	cond := externalEgressCondition(t, c, server)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ExternalEgressReasonPendingApproval {
		t.Fatalf("condition = %+v, want False/PendingApproval", cond)
	}
	if !strings.Contains(cond.Message, "www.googleapis.com") {
		t.Fatalf("message %q should name the requested hosts", cond.Message)
	}
}

func TestExternalEgressApprovalValueMustBeTrue(t *testing.T) {
	server := externalEgressServer(false, []string{"www.googleapis.com"}, nil)
	server.Annotations = map[string]string{EgressApprovedAnnotation: "yes"}
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if _, ok := getExternalEgressPolicy(t, c, server); ok {
		t.Fatal(`only the exact value "true" may approve egress`)
	}
}

func TestExternalEgressRevokedApprovalDeletesPolicy(t *testing.T) {
	server := externalEgressServer(true, []string{"www.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	latest := &mcpv1alpha1.MCPServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(server), latest); err != nil {
		t.Fatal(err)
	}
	delete(latest.Annotations, EgressApprovedAnnotation)
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileExternalEgress(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := getExternalEgressPolicy(t, c, server); ok {
		t.Fatal("policy must be deleted when approval is revoked")
	}
	if cond := externalEgressCondition(t, c, server); cond == nil || cond.Reason != ExternalEgressReasonPendingApproval {
		t.Fatalf("condition = %+v, want PendingApproval after revoke", cond)
	}
}

func TestExternalEgressRemovedDeletesPolicyAndReportsNotRequested(t *testing.T) {
	server := externalEgressServer(true, []string{"www.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	latest := &mcpv1alpha1.MCPServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(server), latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Egress = nil
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileExternalEgress(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := getExternalEgressPolicy(t, c, server); ok {
		t.Fatal("policy must be deleted when spec.egress is removed")
	}
	if cond := externalEgressCondition(t, c, server); cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ExternalEgressReasonNotRequested {
		t.Fatalf("condition = %+v, want True/NotRequested", cond)
	}
}

func TestExternalEgressNotRequestedWritesNoConditionOnPlainServer(t *testing.T) {
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "mcp-servers-org"}}
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if cond := externalEgressCondition(t, c, server); cond != nil {
		t.Fatalf("server without spec.egress should not get a condition, got %+v", cond)
	}
}

func TestExternalEgressRepairsDrift(t *testing.T) {
	server := externalEgressServer(true, []string{"www.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	policy, _ := getExternalEgressPolicy(t, c, server)
	policy.Spec.Egress = nil
	if err := c.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	repaired, _ := getExternalEgressPolicy(t, c, server)
	if len(repaired.Spec.Egress) != 1 {
		t.Fatalf("drifted policy was not repaired: %+v", repaired.Spec.Egress)
	}
}

func TestExternalEgressInvalidSpecReportsConditionWithoutPolicy(t *testing.T) {
	cases := map[string]struct {
		hosts []string
		ports []int32
		want  string
	}{
		"wildcard":    {hosts: []string{"*.googleapis.com"}, want: "wildcard"},
		"ipv4":        {hosts: []string{"8.8.8.8"}, want: "IP"},
		"ipv6":        {hosts: []string{"2001:db8::1"}, want: "IP"},
		"empty":       {hosts: nil, want: "at least one"},
		"blank":       {hosts: []string{" "}, want: "DNS"},
		"scheme":      {hosts: []string{"https://www.googleapis.com"}, want: "DNS"},
		"underscore":  {hosts: []string{"bad_host.example.com"}, want: "DNS"},
		"too many":    {hosts: manyHosts(33), want: "32"},
		"bad port":    {hosts: []string{"a.example.com"}, ports: []int32{0}, want: "port"},
		"high port":   {hosts: []string{"a.example.com"}, ports: []int32{70000}, want: "port"},
		"long label":  {hosts: []string{strings.Repeat("a", 64) + ".example.com"}, want: "DNS"},
		"trailing":    {hosts: []string{"-a.example.com"}, want: "DNS"},
		"too many pt": {hosts: []string{"a.example.com"}, ports: []int32{1, 2, 3, 4, 5, 6, 7, 8, 9}, want: "ports"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := externalEgressServer(true, tc.hosts, tc.ports)
			r, c := externalEgressFixture(t, server)
			if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
				t.Fatal(err)
			}
			if _, ok := getExternalEgressPolicy(t, c, server); ok {
				t.Fatal("invalid egress must not create a NetworkPolicy")
			}
			cond := externalEgressCondition(t, c, server)
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ExternalEgressReasonInvalid {
				t.Fatalf("condition = %+v, want False/Invalid", cond)
			}
			if !strings.Contains(cond.Message, tc.want) {
				t.Fatalf("message %q should mention %q", cond.Message, tc.want)
			}
		})
	}
}

func TestExternalEgressInvalidSpecDeletesExistingPolicy(t *testing.T) {
	server := externalEgressServer(true, []string{"www.googleapis.com"}, nil)
	r, c := externalEgressFixture(t, server)
	if err := r.reconcileExternalEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	latest := &mcpv1alpha1.MCPServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(server), latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Egress.Hosts = []string{"*.googleapis.com"}
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileExternalEgress(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := getExternalEgressPolicy(t, c, server); ok {
		t.Fatal("an invalid spec must not leave an old policy in place")
	}
}

func TestExternalEgressPolicyNameIsDeterministicAndBounded(t *testing.T) {
	if got := externalEgressPolicyName("youtube"); got != "youtube-external-egress" {
		t.Fatalf("name = %q", got)
	}
	long := strings.Repeat("a", 250)
	got := externalEgressPolicyName(long)
	if len(got) > 253 || !strings.HasSuffix(got, "-external-egress") {
		t.Fatalf("long name %q (len %d) must stay within 253 and keep the suffix", got, len(got))
	}
}

func manyHosts(n int) []string {
	hosts := make([]string, n)
	for i := range hosts {
		hosts[i] = "host" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".example.com"
	}
	return hosts
}
