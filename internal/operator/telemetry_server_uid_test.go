package operator

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

// Trace isolation keys on the immutable server UID, because a name and
// namespace can be reused after a server is deleted. Every container that
// exports spans must carry it as a resource attribute.
func TestReconcileDeploymentStampsServerUIDOnTelemetryResources(t *testing.T) {
	replicas := int32(1)
	server := mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "trace-server", Namespace: "mcp-servers-org", UID: types.UID("uid-from-this-instance")},
		Spec:       mcpv1alpha1.MCPServerSpec{Image: "example.com/s", ImageTag: "v1", Port: 8088, ServicePort: 80, Replicas: &replicas},
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{mcpv1alpha1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&server).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme, GatewayProxyImage: "example.com/mcp-gateway:latest", GatewayOTLPEndpoint: "http://otel-collector:4318"}
	if err := r.reconcileDeployment(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, &deployment); err != nil {
		t.Fatal(err)
	}
	want := "service.namespace=mcp-servers-org,mcp.server.uid=uid-from-this-instance"
	seen := 0
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name != "OTEL_RESOURCE_ATTRIBUTES" {
				continue
			}
			seen++
			if env.Value != want {
				t.Errorf("container %s OTEL_RESOURCE_ATTRIBUTES = %q, want %q", container.Name, env.Value, want)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("expected the server and the gateway sidecar to carry OTEL_RESOURCE_ATTRIBUTES, found %d", seen)
	}
}

func TestServerWorkloadKeepsAnExplicitResourceAttributesChoice(t *testing.T) {
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns", UID: "u"},
		Spec:       mcpv1alpha1.MCPServerSpec{EnvVars: []mcpv1alpha1.EnvVar{{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "custom=1"}}},
	}
	r := MCPServerReconciler{GatewayOTLPEndpoint: "http://otel-collector:4318"}
	for _, env := range r.buildServerEnvVars(server) {
		if env.Name == "OTEL_RESOURCE_ATTRIBUTES" && !strings.Contains(env.Value, "custom=1") {
			t.Fatalf("an explicit workload choice was overwritten: %q", env.Value)
		}
	}
}
