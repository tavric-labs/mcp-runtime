package platform

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func connectorConfigMap(data string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth-connectors", Namespace: "mcp-platform"},
		Data:       map[string]string{"connectors.json": data},
	}
}

func connectorNames(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("connectors are not a JSON object: %v: %s", err, raw)
	}
	return doc
}

// setup re-renders the connectors ConfigMap from the main connector; entries the
// gateway and resource commands own must survive, or mcp-auth exits at startup
// because a login method still names the missing connector.
func TestSetupConnectorsKeepOwnedEntriesFromTheLiveConfigMap(t *testing.T) {
	client := fake.NewSimpleClientset(connectorConfigMap(
		`{"keycloak":{"issuer":"old"},"gateway-login-demo":{"issuer":"https://demo/login"},"resource-login-api":{"issuer":"https://idp"},"stale":{"issuer":"x"}}`))
	got, err := connectorsPreservingOwned(context.Background(), client, []byte(`{"keycloak":{"issuer":"new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	doc := connectorNames(t, got)
	for _, name := range []string{"keycloak", "gateway-login-demo", "resource-login-api"} {
		if _, ok := doc[name]; !ok {
			t.Fatalf("connector %q missing from %s", name, got)
		}
	}
	if _, ok := doc["stale"]; ok {
		t.Fatalf("setup must still replace entries it does not own: %s", got)
	}
	if string(doc["keycloak"]) != `{"issuer":"new"}` {
		t.Fatalf("main connector = %s, want the file's", doc["keycloak"])
	}
}

func TestSetupConnectorsOnAFreshInstallAreUnchanged(t *testing.T) {
	in := []byte("{\"keycloak\":{\"issuer\":\"new\"}}\n")
	got, err := connectorsPreservingOwned(context.Background(), fake.NewSimpleClientset(), in)
	if err != nil || string(got) != string(in) {
		t.Fatalf("got %q, %v; want the input unchanged", got, err)
	}
	// A ConfigMap without owned entries also leaves the file as written.
	client := fake.NewSimpleClientset(connectorConfigMap(`{"keycloak":{"issuer":"old"}}`))
	got, err = connectorsPreservingOwned(context.Background(), client, in)
	if err != nil || string(got) != string(in) {
		t.Fatalf("got %q, %v; want the input unchanged", got, err)
	}
}
