package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestSessionsAreNotKubernetesResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"MCPAgentSession", "MCPAgentSessionList"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); !runtime.IsNotRegisteredError(err) {
			t.Errorf("%s must not be a Kubernetes resource; got %v", kind, err)
		}
	}
}
