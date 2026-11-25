package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
)

// A grant's primitive rules must reach the gateway document, and the document
// must require a schema version an older gateway rejects, so the rules are
// never silently dropped.
func TestRenderGatewayPolicyCarriesPrimitiveRules(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "docs", Namespace: "servers"},
		Spec:       mcpv1alpha1.MCPServerSpec{Tools: []mcpv1alpha1.ToolConfig{{Name: "search", SideEffect: mcpv1alpha1.ToolSideEffectRead}}},
	}
	grant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "servers"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "docs", Namespace: "servers"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agent-a"},
			PrimitiveRules: []mcpv1alpha1.PrimitiveRule{
				{Kind: "resource", Pattern: "file:///docs/*", Decision: "allow"},
				{Kind: "prompt", Pattern: "deploy", Decision: "deny"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&mcpv1alpha1.MCPAccessGrant{}, grantTargetServerIndex, grantTargetServerKeys).WithObjects(server, grant).Build()
	r := MCPServerReconciler{GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme}
	doc, err := r.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Grants) != 1 || len(doc.Grants[0].PrimitiveRules) != 2 {
		t.Fatalf("rendered grants = %+v", doc.Grants)
	}
	got := doc.Grants[0].PrimitiveRules
	if got[0] != (policy.PrimitiveRule{Kind: "resource", Pattern: "file:///docs/*", Decision: "allow"}) || got[1].Decision != "deny" {
		t.Fatalf("rules = %+v", got)
	}
	if doc.SchemaVersion != policy.SchemaVersionPrimitiveRules {
		t.Fatalf("SchemaVersion = %q, want %q", doc.SchemaVersion, policy.SchemaVersionPrimitiveRules)
	}
}
