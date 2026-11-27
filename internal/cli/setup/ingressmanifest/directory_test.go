package ingressmanifest_test

import (
	"io"
	"os"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"mcp-runtime/internal/cli/setup/ingressmanifest"
)

func TestDirectoryIngressKeepsPlatformAudienceOnBaseAndRenderedRoutes(t *testing.T) {
	base, err := os.ReadFile("../../../../k8s/10-gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{"base": string(base), "rendered": ingressmanifest.RenderPlatformUIIngress("platform.example.test", "", false, testPlatformNS, testObservabilityNS)} {
		t.Run(name, func(t *testing.T) {
			found := map[string]bool{}
			decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
			for {
				var ingress networkingv1.Ingress
				if err := decoder.Decode(&ingress); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if ingress.Kind != "Ingress" {
					continue
				}
				for _, rule := range ingress.Spec.Rules {
					if rule.HTTP == nil {
						continue
					}
					for _, path := range rule.HTTP.Paths {
						if path.Path != "/api/v1/teams" && path.Path != "/api/v1/operations" {
							continue
						}
						found[path.Path] = true
						if path.Backend.Service == nil || path.Backend.Service.Name != "mcp-platform-api" || path.Backend.Service.Port.Number != 8080 {
							t.Fatalf("directory route reaches wrong service: %+v", path)
						}
						if strings.Contains(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"], "runtime-token-exchange") {
							t.Fatal("platform directory must retain platform token audience")
						}
					}
				}
			}
			for _, path := range []string{"/api/v1/teams", "/api/v1/operations"} {
				if !found[path] {
					t.Errorf("directory route missing: %s", path)
				}
			}
		})
	}
}
