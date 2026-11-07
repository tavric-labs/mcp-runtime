package authgateway

import (
	"encoding/json"
	"strings"
	"testing"
)

const exampleSpec = `{
  "id": "example-api",
  "resource_url": "https://mcp.example.com/example-api/mcp",
  "required_scopes": ["tools:read", "tools:write"],
  "resource_client": {"client_id": "example-mcp", "public_key_pem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"},
  "login": {"label": "Sign in with Example", "downstream_token": "upstream_session"},
  "connector": {
    "issuer": "https://idp.example.com/oauth",
    "authorization_endpoint": "https://idp.example.com/oauth/authorize",
    "token_endpoint": "https://idp.example.com/oauth/token",
    "jwks_uri": "https://idp.example.com/oauth/jwks",
    "client_id": "example-client-id",
    "client_secret_env": "EXAMPLE_IDP_CLIENT_SECRET",
    "token_endpoint_auth_method": "client_secret_post",
    "exchange_client_id": "example-client-id",
    "scopes": ["openid", "profile", "api:write"]
  }
}`

func mutateSpec(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal([]byte(exampleSpec), &spec); err != nil {
		t.Fatal(err)
	}
	edit(spec)
	out, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseResourceSpecAcceptsAnOAuthResource(t *testing.T) {
	spec, err := ParseResourceSpec([]byte(exampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "example-api" || spec.ResourceURL != "https://mcp.example.com/example-api/mcp" || spec.Login.DownstreamToken != "upstream_session" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestParseResourceSpecRejectsWhatCannotWork(t *testing.T) {
	cases := map[string]func(map[string]any){
		"bad id":           func(s map[string]any) { s["id"] = "Not Valid" },
		"long id":          func(s map[string]any) { s["id"] = strings.Repeat("a", 28) },
		"http resource":    func(s map[string]any) { s["resource_url"] = "http://mcp.example.com/example-api/mcp" },
		"root resource":    func(s map[string]any) { s["resource_url"] = "https://mcp.example.com/" },
		"no scopes":        func(s map[string]any) { s["required_scopes"] = []string{} },
		"scope with space": func(s map[string]any) { s["required_scopes"] = []string{"tools read"} },
		"no client id": func(s map[string]any) {
			s["resource_client"] = map[string]any{"public_key_pem": "-----BEGIN PUBLIC KEY-----\nx\n-----END PUBLIC KEY-----"}
		},
		"not a public key": func(s map[string]any) {
			s["resource_client"] = map[string]any{"client_id": "c", "public_key_pem": "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"}
		},
		"unknown strategy":   func(s map[string]any) { s["login"] = map[string]any{"downstream_token": "rfc8693"} },
		"no connector":       func(s map[string]any) { delete(s, "connector") },
		"unknown field":      func(s map[string]any) { s["colour"] = "blue" },
		"no secret env":      func(s map[string]any) { delete(s["connector"].(map[string]any), "client_secret_env") },
		"inline secret":      func(s map[string]any) { s["connector"].(map[string]any)["client_secret"] = "hunter2" },
		"invalid secret env": func(s map[string]any) { s["connector"].(map[string]any)["client_secret_env"] = "not valid" },
	}
	for name, edit := range cases {
		if _, err := ParseResourceSpec(mutateSpec(t, edit)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestResourceWiringDescribesTheResourceToMcpAuth(t *testing.T) {
	spec, err := ParseResourceSpec([]byte(exampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	w := spec.Wiring()
	if w.Method["id"] != "res-example-api" || w.Method["type"] != "oidc" || w.Method["connector"] != "resource-login-example-api" ||
		w.Method["subject_prefix"] != "res-example-api:" || w.Method["downstream_token"] != "upstream_session" {
		t.Fatalf("method = %v", w.Method)
	}
	if resources, _ := w.Method["resources"].([]string); len(resources) != 1 || resources[0] != spec.ResourceURL {
		t.Fatalf("method resources = %v", w.Method["resources"])
	}
	if w.Client["client_id"] != "example-mcp" || w.Client["resource"] != spec.ResourceURL || w.Client["name"] != "resource:example-api" {
		t.Fatalf("client = %v", w.Client)
	}
	if w.ConnectorName != "resource-login-example-api" || !strings.Contains(string(w.Connector), "EXAMPLE_IDP_CLIENT_SECRET") {
		t.Fatalf("connector %q = %s", w.ConnectorName, w.Connector)
	}
}

func TestResourceWiringOmitsDownstreamTokenWhenNotRequested(t *testing.T) {
	spec, err := ParseResourceSpec(mutateSpec(t, func(s map[string]any) { s["login"] = map[string]any{} }))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := spec.Wiring().Method["downstream_token"]; set {
		t.Fatal("a login that did not ask for a downstream token got one")
	}
}

func methodIDs(t *testing.T, document string) []string {
	t.Helper()
	var parsed struct {
		Methods []map[string]any `json:"methods"`
	}
	if err := json.Unmarshal([]byte(document), &parsed); err != nil {
		t.Fatalf("%v: %s", err, document)
	}
	ids := make([]string, 0, len(parsed.Methods))
	for _, m := range parsed.Methods {
		ids = append(ids, m["id"].(string))
	}
	return ids
}

const gatewayLogins = `{"methods": [{"id": "form-demo", "type": "oidc", "connector": "gateway-login-demo", "subject_prefix": "form-demo:", "resources": ["https://auth.example.com/demo/mcp"]}]}`

func TestMergeResourceLoginsKeepsOtherOwnersAndReplacesItsOwn(t *testing.T) {
	spec, _ := ParseResourceSpec([]byte(exampleSpec))
	merged, err := MergeResourceLogins(gatewayLogins, spec.Wiring().Method)
	if err != nil {
		t.Fatal(err)
	}
	if ids := methodIDs(t, merged); strings.Join(ids, ",") != "form-demo,res-example-api" {
		t.Fatalf("ids = %v", ids)
	}
	again, err := MergeResourceLogins(merged, spec.Wiring().Method)
	if err != nil || again != merged {
		t.Fatalf("re-applying changed the file: %v\n%s\n%s", err, merged, again)
	}
	empty, err := MergeResourceLogins("", spec.Wiring().Method)
	if err != nil || strings.Join(methodIDs(t, empty), ",") != "res-example-api" {
		t.Fatalf("from nothing: %v %s", err, empty)
	}
	if _, err := MergeResourceLogins("not json", spec.Wiring().Method); err == nil {
		t.Fatal("an unreadable existing file was overwritten")
	}
}

func TestPreserveForeignLoginsKeepsWhatTheGatewayDoesNotOwn(t *testing.T) {
	spec, _ := ParseResourceSpec([]byte(exampleSpec))
	existing, _ := MergeResourceLogins(gatewayLogins, spec.Wiring().Method)
	regenerated := `{"methods": [{"id": "form-other", "type": "oidc", "connector": "gateway-login-other", "subject_prefix": "form-other:", "resources": ["https://auth.example.com/other/mcp"]}]}`
	merged, err := PreserveForeignLogins(existing, regenerated)
	if err != nil {
		t.Fatal(err)
	}
	// The gateway's own methods are exactly what it generated; the stale form-demo is gone and res-example-api stays.
	if ids := methodIDs(t, merged); strings.Join(ids, ",") != "form-other,res-example-api" {
		t.Fatalf("ids = %v", ids)
	}
	if kept, err := PreserveForeignLogins("", regenerated); err != nil || kept != regenerated {
		t.Fatalf("no existing file must leave the generated one unchanged: %v", err)
	}
}

func TestMergeResourceClientsKeepsOtherClients(t *testing.T) {
	spec, _ := ParseResourceSpec([]byte(exampleSpec))
	existing := `[{"client_id": "someone-else", "name": "manual", "resource": "https://mcp.example.com/other/mcp", "public_key_pem": "x"}]`
	merged, err := MergeResourceClients(existing, spec.Wiring().Client)
	if err != nil {
		t.Fatal(err)
	}
	var clients []map[string]any
	if err := json.Unmarshal([]byte(merged), &clients); err != nil || len(clients) != 2 {
		t.Fatalf("%v: %s", err, merged)
	}
	again, err := MergeResourceClients(merged, spec.Wiring().Client)
	if err != nil || again != merged {
		t.Fatalf("re-applying changed the file: %v", err)
	}
}

func TestMergeResourceConnectorRefusesToReplaceAConnectorItDoesNotOwn(t *testing.T) {
	spec, _ := ParseResourceSpec([]byte(exampleSpec))
	w := spec.Wiring()
	merged, err := MergeResourceConnector(`{"keycloak": {"issuer": "https://kc"}}`, w.ConnectorName, w.Connector)
	if err != nil {
		t.Fatal(err)
	}
	var connectors map[string]json.RawMessage
	if err := json.Unmarshal([]byte(merged), &connectors); err != nil || len(connectors) != 2 || connectors["keycloak"] == nil || connectors[w.ConnectorName] == nil {
		t.Fatalf("%v: %s", err, merged)
	}
	// Another resource's owned entry is neither replaced nor in the way.
	other, err := MergeResourceConnector(`{"resource-login-other": {"issuer": "x"}}`, w.ConnectorName, w.Connector)
	if err != nil || !strings.Contains(other, "resource-login-other") || !strings.Contains(other, w.ConnectorName) {
		t.Fatalf("another resource's connector was lost or blocked the merge: %v\n%s", err, other)
	}
	if _, err := MergeResourceConnector(`{}`, "not-owned-name", w.Connector); err == nil {
		t.Fatal("a name outside the owned prefix was accepted")
	}
	clash := spec.Wiring()
	clash.ConnectorName = "keycloak"
	if _, err := MergeResourceConnector(`{"keycloak": {"issuer": "https://kc"}}`, clash.ConnectorName, clash.Connector); err == nil {
		t.Fatal("the installation's own connector was replaced")
	}
}
