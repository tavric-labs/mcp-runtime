package authgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// The names Runtime owns when it wires an OAuth resource into mcp-auth. Every
// merge below replaces only entries it owns, so the installation's own
// connectors, clients and login methods, and the credential gateway's, are kept
// exactly as written.
const (
	// ResourceMethodPrefix starts the id of every login method the resource
	// command writes, as FormMethodPrefix does for the credential gateway.
	ResourceMethodPrefix = "res-"
	// FormMethodPrefix starts the id of every login method the credential
	// gateway writes (LoginMethodID).
	FormMethodPrefix = "form-"
	// ResourceClientNamePrefix marks the resource clients the command owns.
	ResourceClientNamePrefix = "resource:"
	// ResourceConnectorPrefix starts the name of every connector entry the
	// command owns.
	ResourceConnectorPrefix = "resource-login-"
	// ResourceClientsKey is the resource clients file's key in the wiring ConfigMap.
	ResourceClientsKey = "resource-clients.json"
	// ResourceClientsFile is the clients file's path as mcp-auth sees it.
	ResourceClientsFile = WiringMountPath + "/" + ResourceClientsKey
)

var (
	resourceIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,26}$`)
	secretEnvPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	forbiddenScopeChars = " \t\r\n\"\\"
)

// ResourceSpec describes one MCP server that signs people in through its own
// mcp-auth login and may receive their upstream token. It holds no secret: the
// connector names the environment variable carrying its client secret, and the
// resource client carries only a public key.
type ResourceSpec struct {
	// ID names the resource. The login method is "res-<id>" (at most 32 characters).
	ID string `json:"id"`
	// ResourceURL is the server's canonical public MCP URL, the token audience.
	ResourceURL    string   `json:"resource_url"`
	RequiredScopes []string `json:"required_scopes"`
	ResourceClient struct {
		ClientID     string `json:"client_id"`
		PublicKeyPEM string `json:"public_key_pem"`
	} `json:"resource_client"`
	Login struct {
		Label string `json:"label"`
		// DownstreamToken is "upstream_session" to hand the provider's token to
		// the resource's server, or empty for a login that only identifies.
		DownstreamToken string `json:"downstream_token"`
	} `json:"login"`
	// Connector is an mcp-auth OIDC or OAuth 2.0 connector entry without secrets.
	Connector json.RawMessage `json:"connector"`
}

// ParseResourceSpec reads and validates a resource spec. Unknown fields are an
// error, so a misspelled setting fails instead of being ignored.
func ParseResourceSpec(data []byte) (*ResourceSpec, error) {
	if len(data) > 128<<10 {
		return nil, errors.New("resource spec exceeds 128 KiB")
	}
	var spec ResourceSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parse resource spec: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("resource spec must contain one JSON object")
	}
	if !resourceIDPattern.MatchString(spec.ID) {
		return nil, errors.New("resource id must be lowercase letters, digits and hyphens, at most 27 characters")
	}
	resource, err := resourceURL(spec.ResourceURL)
	if err != nil || resource.Path == "" || resource.Path == "/" || path.Clean(resource.Path) != resource.Path {
		return nil, errors.New("resource_url must be an HTTPS URL with a path, such as https://mcp.example.com/example-api/mcp (plain http is accepted only for localhost test clusters)")
	}
	if len(spec.RequiredScopes) == 0 {
		return nil, errors.New("required_scopes needs at least one scope")
	}
	for _, scope := range spec.RequiredScopes {
		if scope == "" || strings.ContainsAny(scope, forbiddenScopeChars) {
			return nil, errors.New("invalid OAuth scope in required_scopes")
		}
	}
	if spec.ResourceClient.ClientID == "" {
		return nil, errors.New("resource_client.client_id is required")
	}
	if !strings.Contains(spec.ResourceClient.PublicKeyPEM, "BEGIN PUBLIC KEY") {
		return nil, errors.New("resource_client.public_key_pem must be a PEM public key")
	}
	switch spec.Login.DownstreamToken {
	case "", "upstream_session":
	default:
		return nil, fmt.Errorf("login.downstream_token %q is not supported (only \"upstream_session\")", spec.Login.DownstreamToken)
	}
	return &spec, validateResourceConnector(spec.Connector)
}

// resourceURL accepts an HTTPS URL, or a plain http URL whose host is a loopback
// address, which is what a local test cluster serves. MCP Auth applies the same
// rule to its own local development mode; any other http host is refused.
func resourceURL(raw string) (*url.URL, error) {
	if u, err := httpsURL(raw); err == nil {
		return u, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("invalid resource URL")
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return u, nil
	}
	return nil, errors.New("plain http is only allowed for a loopback host")
}

func validateResourceConnector(raw json.RawMessage) error {
	var connector map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &connector) != nil || connector == nil {
		return errors.New("connector must be a JSON object")
	}
	if _, inline := connector["client_secret"]; inline {
		return errors.New("connector must not contain client_secret; name an environment variable in client_secret_env")
	}
	var env string
	if json.Unmarshal(connector["client_secret_env"], &env) != nil || !secretEnvPattern.MatchString(env) {
		return errors.New("connector.client_secret_env must name the environment variable that holds the client secret")
	}
	return nil
}

// ResourceWiring is what mcp-auth must know about one resource.
type ResourceWiring struct {
	// Method is the login method entry for the resource logins file.
	Method map[string]any
	// Client is the resource client entry for the resource clients file.
	Client map[string]any
	// ConnectorName and Connector are the entry to merge into the connectors.
	ConnectorName string
	Connector     json.RawMessage
}

// ResourceMethodID names the resource's login method in mcp-auth.
func ResourceMethodID(id string) string { return ResourceMethodPrefix + id }

// ResourceConnectorName names the resource's connector entry.
func ResourceConnectorName(id string) string { return ResourceConnectorPrefix + id }

// Wiring derives the mcp-auth side of the resource.
func (s *ResourceSpec) Wiring() ResourceWiring {
	method := map[string]any{
		"id": ResourceMethodID(s.ID), "type": "oidc", "connector": ResourceConnectorName(s.ID),
		"subject_prefix": ResourceMethodID(s.ID) + ":", "resources": []string{s.ResourceURL},
	}
	if s.Login.Label != "" {
		method["label"] = s.Login.Label
	}
	if s.Login.DownstreamToken != "" {
		method["downstream_token"] = s.Login.DownstreamToken
	}
	return ResourceWiring{
		Method: method,
		Client: map[string]any{
			"client_id": s.ResourceClient.ClientID, "name": ResourceClientNamePrefix + s.ID,
			"resource": s.ResourceURL, "public_key_pem": strings.TrimSpace(s.ResourceClient.PublicKeyPEM) + "\n",
		},
		ConnectorName: ResourceConnectorName(s.ID),
		Connector:     s.Connector,
	}
}

type loginsDocument struct {
	Methods []map[string]any `json:"methods"`
}

func readLogins(existing string) (loginsDocument, error) {
	var doc loginsDocument
	if strings.TrimSpace(existing) == "" {
		return doc, nil
	}
	if err := json.Unmarshal([]byte(existing), &doc); err != nil {
		return doc, errors.New("the installed resource logins file is not a JSON object with a methods list")
	}
	return doc, nil
}

func writeLogins(doc loginsDocument) (string, error) {
	if doc.Methods == nil {
		doc.Methods = []map[string]any{}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	return string(out) + "\n", err
}

// MergeResourceLogins returns the resource logins file with method added or
// replaced by id. Every other method, including the credential gateway's, is
// kept as written.
func MergeResourceLogins(existing string, method map[string]any) (string, error) {
	doc, err := readLogins(existing)
	if err != nil {
		return "", err
	}
	id, _ := method["id"].(string)
	kept := doc.Methods[:0:0]
	for _, m := range doc.Methods {
		if m["id"] != id {
			kept = append(kept, m)
		}
	}
	doc.Methods = append(kept, method)
	return writeLogins(doc)
}

// PreserveForeignLogins returns the credential gateway's regenerated logins
// file plus every method in the existing file the gateway does not own, that
// is, every id without the gateway's "form-" prefix. Without it, re-applying
// the gateway would erase the resource logins written beside its own.
func PreserveForeignLogins(existing, generated string) (string, error) {
	old, err := readLogins(existing)
	if err != nil {
		return "", err
	}
	foreign := []map[string]any{}
	for _, m := range old.Methods {
		if id, _ := m["id"].(string); !strings.HasPrefix(id, FormMethodPrefix) {
			foreign = append(foreign, m)
		}
	}
	if len(foreign) == 0 {
		return generated, nil
	}
	doc, err := readLogins(generated)
	if err != nil {
		return "", err
	}
	doc.Methods = append(doc.Methods, foreign...)
	return writeLogins(doc)
}

// MergeResourceClients returns the resource clients file with client added or
// replaced by name. Clients it does not own are kept as written.
func MergeResourceClients(existing string, client map[string]any) (string, error) {
	var clients []map[string]any
	if strings.TrimSpace(existing) != "" && json.Unmarshal([]byte(existing), &clients) != nil {
		return "", errors.New("the installed resource clients file is not a JSON array")
	}
	name, _ := client["name"].(string)
	kept := clients[:0:0]
	for _, c := range clients {
		if c["name"] != name {
			kept = append(kept, c)
		}
	}
	out, err := json.MarshalIndent(append(kept, client), "", "  ")
	return string(out) + "\n", err
}

// MergeResourceConnector returns the connectors with the resource's entry added
// or replaced. Every other entry is kept as written, and an existing entry the
// command does not own is never replaced.
func MergeResourceConnector(existing, name string, entry json.RawMessage) (string, error) {
	if !strings.HasPrefix(name, ResourceConnectorPrefix) {
		return "", fmt.Errorf("connector %q is not named with the %q prefix Runtime owns", name, ResourceConnectorPrefix)
	}
	connectors := map[string]json.RawMessage{}
	if strings.TrimSpace(existing) != "" && json.Unmarshal([]byte(existing), &connectors) != nil {
		return "", errors.New("the installed connectors file is not a JSON object")
	}
	connectors[name] = entry
	out, err := json.MarshalIndent(connectors, "", "  ")
	return string(out) + "\n", err
}
