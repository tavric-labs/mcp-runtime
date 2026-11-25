package runtimeapi

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/k8sclient"
)

func TestTeamIntentProvisionerConfig(t *testing.T) {
	var nilSvc *DeploymentService
	if _, ok := nilSvc.TeamIntentProvisionerConfig(); ok {
		t.Fatal("nil DeploymentService should report unavailable")
	}

	emptySvc := &DeploymentService{}
	if _, ok := emptySvc.TeamIntentProvisionerConfig(); ok {
		t.Fatal("DeploymentService without k8s client should report unavailable")
	}

	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "required")
	t.Setenv("PLATFORM_TRAEFIK_NAMESPACE", "traefik")
	t.Setenv("PLATFORM_TRAEFIK_DEPLOYMENT", "traefik")
	t.Setenv("PLATFORM_TRAEFIK_SERVICE_ACCOUNT", "traefik")

	k8s := fake.NewSimpleClientset()
	svc := &DeploymentService{k8sClients: &k8sclient.Clients{Clientset: k8s}}
	cfg, ok := svc.TeamIntentProvisionerConfig()
	if !ok {
		t.Fatal("expected TeamIntentProvisionerConfig to succeed")
	}
	if cfg.Client != k8s {
		t.Fatal("Client mismatch")
	}
	if cfg.EnsureRegistryPullSecret == nil {
		t.Fatal("EnsureRegistryPullSecret must be wired")
	}
	if cfg.Traefik.Mode != "required" || cfg.Traefik.Namespace != "traefik" {
		t.Fatalf("unexpected Traefik config: %+v", cfg.Traefik)
	}
	if len(cfg.IngressFromNamespaces) != 1 || cfg.IngressFromNamespaces[0] != "traefik" {
		t.Fatalf("unexpected IngressFromNamespaces: %+v", cfg.IngressFromNamespaces)
	}
}
