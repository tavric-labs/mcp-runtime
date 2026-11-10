package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/authgateway"
)

const guardKeycloakOnly = `{"keycloak":{"issuer":"https://kc/realms/x","client_secret_env":"KC_SECRET"}}`

// gatewayWithLostConnector is the live incident: the form login method is still
// in the wiring ConfigMap while the connectors ConfigMap lost its entry.
func gatewayWithLostConnector(t *testing.T) *fake.Clientset {
	t.Helper()
	ctx := context.Background()
	c := resourceCluster(nil)
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	setConnectors(t, c, guardKeycloakOnly)
	return c
}

func setConnectors(t *testing.T, c *fake.Clientset, data string) {
	t.Helper()
	cm, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(context.Background(), mcpAuthConnectorName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["connectors.json"] = data
	if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func configMapData(t *testing.T, c *fake.Clientset, name string) map[string]string {
	t.Helper()
	cm, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return cm.Data
}

func requireNoWrites(t *testing.T, c *fake.Clientset, connectorsBefore, wiringBefore map[string]string) {
	t.Helper()
	if got := configMapData(t, c, mcpAuthConnectorName); got["connectors.json"] != connectorsBefore["connectors.json"] {
		t.Fatalf("connectors changed by a refused apply: %v", got)
	}
	if got := configMapData(t, c, authgateway.WiringName); len(got) != len(wiringBefore) || got["resource-logins.json"] != wiringBefore["resource-logins.json"] {
		t.Fatalf("wiring changed by a refused apply: %v", got)
	}
}

func TestResourceApplyRefusesWhenAnExistingLoginHasNoConnector(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		c := gatewayWithLostConnector(t)
		connectors, wiring := configMapData(t, c, mcpAuthConnectorName), configMapData(t, c, authgateway.WiringName)
		_, err := applyResource(context.Background(), c, testResourceInstall(t, dryRun))
		if err == nil {
			t.Fatalf("dryRun=%v: resource apply succeeded with a login method whose connector is missing", dryRun)
		}
		for _, want := range []string{authgateway.LoginMethodID("demo"), authgateway.ConnectorName("demo")} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name %q", err, want)
			}
		}
		requireNoWrites(t, c, connectors, wiring)
	}
}

func TestGatewayApplyRefusesWhenAForeignLoginHasNoConnector(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		ctx := context.Background()
		c := resourceCluster(nil)
		if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
			t.Fatal(err)
		}
		cm, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
		cm.Data["resource-logins.json"] = `{"methods":[{"id":"custom","type":"oidc","connector":"ghost"}]}`
		if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		connectors, wiring := configMapData(t, c, mcpAuthConnectorName), configMapData(t, c, authgateway.WiringName)
		_, err := installGateway(ctx, c, testInstall(t, dryRun))
		if err == nil || !strings.Contains(err.Error(), "custom") || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("dryRun=%v: err = %v; want a refusal naming custom and ghost", dryRun, err)
		}
		requireNoWrites(t, c, connectors, wiring)
	}
}

func TestGatewayApplyStillRepairsALostGatewayConnector(t *testing.T) {
	c := gatewayWithLostConnector(t)
	if _, err := installGateway(context.Background(), c, testInstall(t, false)); err != nil {
		t.Fatalf("gateway apply must repair the lost connector: %v", err)
	}
	if !connectorKeys(t, c)[authgateway.ConnectorName("demo")] {
		t.Fatal("gateway connector was not restored")
	}
}

func TestConnectorApplyAddingTheMissingConnectorRepairsTheState(t *testing.T) {
	c := gatewayWithLostConnector(t)
	repaired := `{"keycloak":{"issuer":"https://kc/realms/x","client_secret_env":"KC_SECRET"},"` + authgateway.ConnectorName("demo") + `":{"issuer":"https://demo/login"}}`
	if _, err := applyMCPAuthConnector(context.Background(), c, repaired, false); err != nil {
		t.Fatalf("the repairing connector apply was blocked: %v", err)
	}
	if !connectorKeys(t, c)[authgateway.ConnectorName("demo")] {
		t.Fatal("connector was not added")
	}
}

func TestConnectorApplyRefusesToDropAConnectorALoginStillNames(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		ctx := context.Background()
		c := resourceCluster(nil)
		if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
			t.Fatal(err)
		}
		cm, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
		cm.Data["resource-logins.json"] = `{"methods":[{"id":"corp","type":"oidc","connector":"keycloak"}]}`
		if _, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		setConnectors(t, c, guardKeycloakOnly)
		before := configMapData(t, c, mcpAuthConnectorName)
		_, err := applyMCPAuthConnector(ctx, c, `{"other":{"client_secret_env":"X"}}`, dryRun)
		if err == nil || !strings.Contains(err.Error(), "corp") || !strings.Contains(err.Error(), "keycloak") {
			t.Fatalf("dryRun=%v: err = %v; want a refusal naming corp and keycloak", dryRun, err)
		}
		if configMapData(t, c, mcpAuthConnectorName)["connectors.json"] != before["connectors.json"] {
			t.Fatal("refused connector apply wrote")
		}
	}
}

func TestConnectorApplyWithoutWiringConfigMapIsUnchanged(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: mcpAuthConnectorName, Namespace: mcpAuthNamespace},
		Data:       map[string]string{"connectors.json": guardKeycloakOnly},
	})
	if _, err := applyMCPAuthConnector(context.Background(), c, `{"keycloak":{"client_secret_env":"X"}}`, false); err != nil {
		t.Fatal(err)
	}
}

func TestResourceApplyOnAHealthyGatewayStillSucceeds(t *testing.T) {
	ctx := context.Background()
	c := resourceCluster(nil)
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := applyResource(ctx, c, testResourceInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configMapData(t, c, mcpAuthConnectorName)["connectors.json"]), &doc); err != nil || len(doc) < 3 {
		t.Fatalf("connectors = %v, %v", doc, err)
	}
}

func connectorKeys(t *testing.T, c *fake.Clientset) map[string]bool {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configMapData(t, c, mcpAuthConnectorName)["connectors.json"]), &doc); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range doc {
		keys[k] = true
	}
	return keys
}
