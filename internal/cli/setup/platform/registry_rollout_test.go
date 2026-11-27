package platform

import (
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/cli/core"
)

func TestRegistrySetupDoesNotHandOffToOldAvailableReplicaWhenNewRolloutFailed(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry", Generation: 2},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 2, AvailableReplicas: 1, Replicas: 2, UpdatedReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}},
	}
	clients := newPlatformKubernetesTestClients([]runtime.Object{deployment}, nil)
	clients.Clientset.(*fake.Clientset).PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) { return true, deployment.DeepCopy(), nil })
	swapKubernetesClientsForTest(t, clients)
	resetPlatformKubeconfig(t)
	initPlatformKubeconfig("")
	mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		cmd := &core.MockCommand{}
		if slices.Contains(spec.Args, "kustomize") {
			cmd.RunFunc = func() error {
				_, err := cmd.StdoutW.Write([]byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: registry
spec:
  replicas: 1
  selector:
    matchLabels: {app: registry}
  template:
    metadata:
      labels: {app: registry}
    spec:
      containers:
      - {name: registry, image: 'registry:2.8.3'}
`))
				return err
			}
		}
		return cmd
	}}
	swapDefaultKubectlClientForTest(t, core.NewTestKubectlClient(mock))
	err := deployRegistryClientGo(zap.NewNop(), "registry", 5000, "docker", "", "fixture-manifest")
	if err == nil || !strings.Contains(err.Error(), "rollout") {
		t.Fatalf("setup must fail before publishing when only an old registry replica is available: %v", err)
	}
}
