package k8sstore

import (
	"context"
	"mcp-runtime/pkg/access"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestManagerUsesRuntimeCRDGroup(t *testing.T) {
	if APIGroup != mcpv1alpha1.GroupVersion.Group {
		t.Fatalf("APIGroup = %q, want %q", APIGroup, mcpv1alpha1.GroupVersion.Group)
	}
	if APIVersion != mcpv1alpha1.GroupVersion.Version {
		t.Fatalf("APIVersion = %q, want %q", APIVersion, mcpv1alpha1.GroupVersion.Version)
	}
	if grantGVR.Group != APIGroup || sessionGVR.Group != APIGroup {
		t.Fatalf("expected grant/session GVRs to use APIGroup %q, got %q and %q", APIGroup, grantGVR.Group, sessionGVR.Group)
	}
}

func TestApplyGrantCreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil)

	created, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "grant-a",
			Namespace:   "mcp-servers",
			Labels:      map[string]string{"operator": "owned"},
			Annotations: map[string]string{"note": "keep"},
			Finalizers:  []string{"mcpruntime.org/finalizer"},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "mcpruntime.org/v1alpha1", Kind: "MCPServer", Name: "demo"},
			},
		},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1", AgentID: "agent-1"},
			MaxTrust:  access.TrustLevel("low"),
			ToolRules: []access.ToolRule{{Name: "aaa-ping", Decision: access.DecisionAllow}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyGrant create returned error: %v", err)
	}
	if created.Name != "grant-a" || created.Spec.MaxTrust != access.TrustLevel("low") {
		t.Fatalf("created grant mismatch: %#v", created)
	}

	updated, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-servers"},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1", AgentID: "agent-1"},
			MaxTrust:  access.TrustLevel("high"),
			ToolRules: []access.ToolRule{{Name: "aaa-ping", Decision: access.DecisionDeny}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyGrant update returned error: %v", err)
	}
	if updated.Spec.MaxTrust != access.TrustLevel("high") {
		t.Fatalf("updated MaxTrust = %q, want high", updated.Spec.MaxTrust)
	}
	if got := updated.Spec.ToolRules[0].Decision; got != access.DecisionDeny {
		t.Fatalf("updated decision = %q, want %q", got, access.DecisionDeny)
	}
	if got := updated.Labels["operator"]; got != "owned" {
		t.Fatalf("updated label operator = %q, want owned", got)
	}
	if got := updated.Annotations["note"]; got != "keep" {
		t.Fatalf("updated annotation note = %q, want keep", got)
	}
	if len(updated.Finalizers) != 1 || updated.Finalizers[0] != "mcpruntime.org/finalizer" {
		t.Fatalf("updated finalizers = %#v, want preserved finalizer", updated.Finalizers)
	}
	if len(updated.OwnerReferences) != 1 || updated.OwnerReferences[0].Name != "demo" {
		t.Fatalf("updated owner references = %#v, want preserved owner reference", updated.OwnerReferences)
	}
}

func TestApplySessionCreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil)

	created, err := manager.ApplySession(ctx, &MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session-a", Namespace: "mcp-servers"},
		Spec: access.MCPAgentSessionSpec{
			ServerRef:      access.ServerReference{Name: "demo"},
			Subject:        access.SubjectRef{HumanID: "user-1", AgentID: "agent-1"},
			ConsentedTrust: access.TrustLevel("low"),
		},
	})
	if err != nil {
		t.Fatalf("ApplySession create returned error: %v", err)
	}
	if created.Name != "session-a" || created.Spec.ConsentedTrust != access.TrustLevel("low") {
		t.Fatalf("created session mismatch: %#v", created)
	}

	updated, err := manager.ApplySession(ctx, &MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session-a", Namespace: "mcp-servers"},
		Spec: access.MCPAgentSessionSpec{
			ServerRef:      access.ServerReference{Name: "demo"},
			Subject:        access.SubjectRef{HumanID: "user-1", AgentID: "agent-1"},
			ConsentedTrust: access.TrustLevel("medium"),
			Revoked:        true,
		},
	})
	if err != nil {
		t.Fatalf("ApplySession update returned error: %v", err)
	}
	if updated.Spec.ConsentedTrust != access.TrustLevel("medium") {
		t.Fatalf("updated ConsentedTrust = %q, want medium", updated.Spec.ConsentedTrust)
	}
	if !updated.Spec.Revoked {
		t.Fatalf("updated Revoked = false, want true")
	}
}

func TestAssertMCPServerRef(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	srv := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "payments", Namespace: "mcp-servers",
		},
	}
	manager := NewManager(dynamicfake.NewSimpleDynamicClient(scheme, srv), nil)

	if err := manager.AssertMCPServerRef(ctx, access.ServerReference{Name: "payments", Namespace: "mcp-servers"}); err != nil {
		t.Fatalf("valid ref: %v", err)
	}

	err := manager.AssertMCPServerRef(ctx, access.ServerReference{Name: "missing", Namespace: "mcp-servers"})
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if !access.IsMCPServerNotFoundForRef(err) {
		t.Fatalf("expected ErrMCPServerNotFound, got %v", err)
	}
}

func TestResolveServerRefNamespace(t *testing.T) {
	if got := access.ResolveServerRefNamespace(access.ServerReference{Namespace: "  \t"}); got != access.DefaultMCPResourceNamespace {
		t.Fatalf("whitespace only namespace = %q, want default", got)
	}
}

func TestApplyGrantRetriesOnConflict(t *testing.T) {
	ctx := context.Background()
	fake := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	manager := NewManager(fake, nil)

	if _, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-servers"},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1"},
			MaxTrust:  access.TrustLevel("low"),
		},
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	var conflicts int
	fake.PrependReactor("update", "mcpaccessgrants", func(clienttesting.Action) (bool, runtime.Object, error) {
		if conflicts < 2 {
			conflicts++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: APIGroup, Resource: "mcpaccessgrants"},
				"grant-a",
				apierrors.NewConflict(schema.GroupResource{}, "", nil),
			)
		}
		return false, nil, nil
	})

	updated, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-servers"},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1"},
			MaxTrust:  access.TrustLevel("high"),
		},
	})
	if err != nil {
		t.Fatalf("ApplyGrant after retries: %v", err)
	}
	if conflicts != 2 {
		t.Fatalf("expected 2 simulated conflicts, observed %d", conflicts)
	}
	if updated.Spec.MaxTrust != access.TrustLevel("high") {
		t.Fatalf("post-retry MaxTrust = %q, want high", updated.Spec.MaxTrust)
	}
}

func TestApplyGrantGivesUpAfterMaxConflicts(t *testing.T) {
	ctx := context.Background()
	fake := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	manager := NewManager(fake, nil)

	if _, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-servers"},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1"},
			MaxTrust:  access.TrustLevel("low"),
		},
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	fake.PrependReactor("update", "mcpaccessgrants", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: APIGroup, Resource: "mcpaccessgrants"},
			"grant-a",
			apierrors.NewConflict(schema.GroupResource{}, "", nil),
		)
	})

	_, err := manager.ApplyGrant(ctx, &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-servers"},
		Spec: access.MCPAccessGrantSpec{
			ServerRef: access.ServerReference{Name: "demo"},
			Subject:   access.SubjectRef{HumanID: "user-1"},
			MaxTrust:  access.TrustLevel("high"),
		},
	})
	if err == nil {
		t.Fatal("expected persistent conflict to surface as error")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected IsConflict error, got %v", err)
	}
}

func TestValidateResourceName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"valid", "grant-a", false},
		{"valid-with-dots", "my.mcp.server", false},
		{"underscores", "grant_a", true},
		{"uppercase", "GrantA", true},
		{"leading-hyphen", "-grant", true},
		{"trailing-hyphen", "grant-", true},
		{"64chars-ok", "g123456789012345678901234567890123456789012345678901234567890123", false},
		{"too-long-254", strings.Repeat("a", 254), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := access.ValidateResourceName("name", tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateResourceName(%q) err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestValidateOptionalResourceName(t *testing.T) {
	if err := access.ValidateOptionalResourceName("namespace", ""); err != nil {
		t.Fatalf("empty optional should be allowed: %v", err)
	}
	if err := access.ValidateOptionalResourceName("namespace", "Bad_Name"); err == nil {
		t.Fatal("expected error for invalid optional name")
	}
}
