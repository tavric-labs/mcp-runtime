package cluster

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/authgateway"
)

const exampleResourceURL = "https://mcp.example.com/example-api/mcp"

const exampleResourceSpec = `{
  "id": "example-api",
  "resource_url": "` + exampleResourceURL + `",
  "required_scopes": ["tools:read", "tools:write"],
  "resource_client": {"client_id": "example-mcp", "public_key_pem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"},
  "login": {"label": "Sign in with Example", "downstream_token": "upstream_session"},
  "connector": {
    "issuer": "https://idp.example.com/oauth",
    "authorization_endpoint": "https://idp.example.com/oauth/authorize",
    "token_endpoint": "https://idp.example.com/oauth/token",
    "client_id": "example-client-id",
    "client_secret_env": "EXAMPLE_IDP_CLIENT_SECRET",
    "token_endpoint_auth_method": "client_secret_post",
    "exchange_client_id": "example-client-id",
    "scopes": ["openid", "profile"]
  }
}`

func testResourceInstall(t *testing.T, dryRun bool) resourceInstall {
	t.Helper()
	spec, err := authgateway.ParseResourceSpec([]byte(exampleResourceSpec))
	if err != nil {
		t.Fatal(err)
	}
	return resourceInstall{spec: spec, dryRun: dryRun}
}

// resourceCluster is an installed MCP Auth that already serves the resource
// (the operator registered it from the MCPServer) and holds the connector secret.
func resourceCluster(mutateAuth func(*appsv1.Deployment), secretKeys ...string) *fake.Clientset {
	if len(secretKeys) == 0 {
		secretKeys = []string{"EXAMPLE_IDP_CLIENT_SECRET"}
	}
	data := map[string][]byte{}
	for _, key := range secretKeys {
		data[key] = []byte("value-never-read")
	}
	c := authCluster(func(d *appsv1.Deployment) {
		envs := d.Spec.Template.Spec.Containers[0].Env
		for i := range envs {
			if envs[i].Name == "MCP_AUTH_RESOURCES" {
				envs[i].Value += "," + exampleResourceURL
			}
		}
		if mutateAuth != nil {
			mutateAuth(d)
		}
	})
	_, _ = c.CoreV1().Secrets(mcpAuthNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: connectorSecretName, Namespace: mcpAuthNamespace}, Data: data,
	}, metav1.CreateOptions{})
	return c
}

func TestApplyResourceWiresTheResourceIntoMcpAuth(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	report, err := applyResource(ctx, c, testResourceInstall(t, false))
	if err != nil || !report.changed {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	wiring, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wiring.Data["resource-logins.json"], "res-example-api") || !strings.Contains(wiring.Data["resource-logins.json"], `"downstream_token": "upstream_session"`) {
		t.Fatalf("logins = %s", wiring.Data["resource-logins.json"])
	}
	if !strings.Contains(wiring.Data["resource-clients.json"], "example-mcp") || !strings.Contains(wiring.Data["resource-clients.json"], exampleResourceURL) {
		t.Fatalf("clients = %s", wiring.Data["resource-clients.json"])
	}
	if !ownedByRuntime(wiring.ObjectMeta) {
		t.Fatal("the wiring ConfigMap was created without Runtime ownership")
	}
	connectors, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	if got := connectors.Data["connectors.json"]; !strings.Contains(got, "resource-login-example-api") || !strings.Contains(got, "keycloak") {
		t.Fatalf("connectors = %s, want the installation's connector kept and the resource's added", got)
	}
	env := envOf(t, c)
	if env["MCP_AUTH_RESOURCE_LOGINS_FILE"] != authgateway.ResourceLoginsFile || env["MCP_AUTH_RESOURCE_CLIENTS_FILE"] != authgateway.ResourceClientsFile || env["KEEP"] != "yes" {
		t.Fatalf("env = %v", env)
	}
	d, _ := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	if d.Spec.Template.Annotations["mcpruntime.org/resource-wiring-hash"] == "" {
		t.Fatal("the pod template has no restart marker, so mcp-auth would not reload the wiring")
	}
	var mounted bool
	for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
		mounted = mounted || (m.MountPath == authgateway.WiringMountPath && m.ReadOnly)
	}
	if !mounted || len(d.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("wiring volume not mounted: mounts=%v volumes=%v", d.Spec.Template.Spec.Containers[0].VolumeMounts, d.Spec.Template.Spec.Volumes)
	}
}

func TestApplyResourceTwiceChangesNothingTheSecondTime(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	first, err := applyResource(ctx, c, testResourceInstall(t, false))
	if err != nil {
		t.Fatal(err)
	}
	second, err := applyResource(ctx, c, testResourceInstall(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if second.changed || first.hash != second.hash {
		t.Fatalf("a repeat apply changed things: %+v then %+v, so mcp-auth would restart for nothing", first, second)
	}
}

func TestApplyResourceDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	report, err := applyResource(ctx, c, testResourceInstall(t, true))
	if err != nil || !report.changed {
		t.Fatalf("err=%v report=%+v, want a dry run to report the change", err, report)
	}
	if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{}); err == nil {
		t.Fatal("a dry run created the wiring ConfigMap")
	}
	if _, set := envOf(t, c)["MCP_AUTH_RESOURCE_CLIENTS_FILE"]; set {
		t.Fatal("a dry run changed the Deployment")
	}
}

func TestApplyResourceKeepsTheGatewaysEntriesAndTheGatewayKeepsTheResources(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := applyResource(ctx, c, testResourceInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	wiring, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	logins := wiring.Data["resource-logins.json"]
	if !strings.Contains(logins, "form-demo") || !strings.Contains(logins, "res-example-api") || wiring.Data["external-credentials.json"] == "" {
		t.Fatalf("applying a resource disturbed the gateway's wiring: %s", logins)
	}
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	again, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	if !strings.Contains(again.Data["resource-logins.json"], "res-example-api") || again.Data["resource-clients.json"] == "" {
		t.Fatal("re-applying the gateway dropped the resource")
	}
}

func TestApplyResourceRefusesWhatWouldBreakMcpAuth(t *testing.T) {
	cases := map[string]struct {
		cluster *fake.Clientset
		want    string
	}{
		"login methods mode": {resourceCluster(func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "MCP_AUTH_LOGIN_METHODS_FILE", Value: "/x.json"})
		}), "login methods mode"},
		"resource not served yet": {authCluster(nil), "deploy the MCP server first"},
		"logins file owned elsewhere": {resourceCluster(func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "MCP_AUTH_RESOURCE_LOGINS_FILE", Value: "/other/logins.json"})
		}), "MCP_AUTH_RESOURCE_LOGINS_FILE"},
		"clients file owned elsewhere": {resourceCluster(func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "MCP_AUTH_RESOURCE_CLIENTS_FILE", Value: "/other/clients.json"})
		}), "MCP_AUTH_RESOURCE_CLIENTS_FILE"},
		"connector secret missing the key": {resourceCluster(nil, "SOME_OTHER_KEY"), "EXAMPLE_IDP_CLIENT_SECRET"},
	}
	for name, tc := range cases {
		_, err := applyResource(context.Background(), tc.cluster, testResourceInstall(t, false))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestApplyResourceNeverReadsOrPrintsSecretValues(t *testing.T) {
	c := resourceCluster(nil, "SOME_OTHER_KEY")
	_, err := applyResource(context.Background(), c, testResourceInstall(t, false))
	if err == nil || strings.Contains(err.Error(), "value-never-read") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyResourceDoesNotTouchAnUnownedWiringConfigMap(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	_, _ = c.CoreV1().ConfigMaps(mcpAuthNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: authgateway.WiringName, Namespace: mcpAuthNamespace}, Data: map[string]string{"x": "y"},
	}, metav1.CreateOptions{})
	if _, err := applyResource(ctx, c, testResourceInstall(t, false)); err == nil {
		t.Fatal("a ConfigMap without Runtime ownership was overwritten")
	}
}

const exampleSpecYAML = `
# An admin-applied file kept beside the MCP server's own metadata.
id: example-api
resource_url: https://mcp.example.com/example-api/mcp
required_scopes: [tools:read, tools:write]
resource_client:
  client_id: example-mcp
  public_key_pem: |
    -----BEGIN PUBLIC KEY-----
    MIIB
    -----END PUBLIC KEY-----
login:
  label: Sign in with Example
  downstream_token: upstream_session
connector:
  issuer: https://idp.example.com/oauth
  authorization_endpoint: https://idp.example.com/oauth/authorize
  token_endpoint: https://idp.example.com/oauth/token
  client_id: example-client-id
  client_secret_env: EXAMPLE_IDP_CLIENT_SECRET
  token_endpoint_auth_method: client_secret_post
  exchange_client_id: example-client-id
  scopes: [openid, profile]
  pkce: false
  id_token_nonce_policy: disabled
`

// The server's .mcp directory holds YAML, so the command accepts a resource file as
// YAML as well as JSON, with the same validation. The conversion lives in the CLI
// adapter: pkg/authgateway is a support package and may not import Kubernetes libraries.
func TestParseResourceFileAcceptsYAML(t *testing.T) {
	spec, err := parseResourceFile([]byte(exampleSpecYAML))
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "example-api" || spec.Login.DownstreamToken != "upstream_session" || !strings.Contains(spec.ResourceClient.PublicKeyPEM, "BEGIN PUBLIC KEY") {
		t.Fatalf("spec = %+v", spec)
	}
	// Provider-specific connector settings pass through untouched for mcp-auth to read.
	if !strings.Contains(string(spec.Connector), `"pkce":false`) || !strings.Contains(string(spec.Connector), `"id_token_nonce_policy":"disabled"`) {
		t.Fatalf("connector settings were lost converting from YAML: %s", spec.Connector)
	}
}

func TestParseResourceFileYAMLStillValidates(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field": exampleSpecYAML + "colour: blue\n",
		"bad yaml":      "id: [unclosed\n",
		"inline secret": strings.Replace(exampleSpecYAML, "client_secret_env: EXAMPLE_IDP_CLIENT_SECRET", "client_secret: hunter2", 1),
		"http resource": strings.Replace(exampleSpecYAML, "https://mcp.example.com", "http://mcp.example.com", 1),
	} {
		if _, err := parseResourceFile([]byte(doc)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestParseResourceFileStillAcceptsJSON(t *testing.T) {
	if _, err := parseResourceFile([]byte(exampleResourceSpec)); err != nil {
		t.Fatalf("an existing JSON resource file stopped working: %v", err)
	}
}
