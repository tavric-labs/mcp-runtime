package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CheckDeploymentReadiness rejects both unavailable workloads and old ready
// replicas while a new rollout has not yet been observed or completed.
func CheckDeploymentReadiness(raw []byte) error {
	var list struct {
		Items []struct {
			Metadata struct {
				Name       string
				Generation int64
			}
			Spec   struct{ Replicas *int32 }
			Status struct {
				ObservedGeneration                                                               int64
				Replicas, ReadyReplicas, AvailableReplicas, UpdatedReplicas, UnavailableReplicas int32
			}
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("invalid deployment inventory")
	}
	if len(list.Items) == 0 {
		return fmt.Errorf("namespace has no deployments")
	}
	for _, d := range list.Items {
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration < d.Metadata.Generation || d.Status.ReadyReplicas < want || d.Status.AvailableReplicas < want || d.Status.UpdatedReplicas != want || d.Status.Replicas != want || d.Status.UnavailableReplicas > 0 {
			return fmt.Errorf("deployment %s has not completed its current rollout", d.Metadata.Name)
		}
	}
	return nil
}

func CheckAuthorizationMetadata(raw []byte, issuer string) error {
	var d struct {
		Issuer string   `json:"issuer"`
		Scopes []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("invalid authorization metadata")
	}
	if d.Issuer != issuer {
		return fmt.Errorf("authorization metadata issuer mismatch")
	}
	scopes := map[string]bool{}
	for _, s := range d.Scopes {
		scopes[s] = true
	}
	if !scopes["tools:read"] || !scopes["tools:write"] {
		return fmt.Errorf("authorization metadata is missing required tool scopes")
	}
	return nil
}

func ValidatePolicyDenial(status int, raw []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	if status != http.StatusForbidden || json.Unmarshal(raw, &body) != nil {
		return fmt.Errorf("ungranted tool did not return an explicit HTTP 403 policy denial")
	}
	switch body.Error {
	case "tool_not_granted", "tool_denied", "tool_not_allowed":
		return nil
	}
	return fmt.Errorf("ungranted tool returned a different failure, not a tool policy denial")
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func request(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	}
	response, err := httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return 0, nil, err
	}
	if len(raw) > 1<<20 {
		return 0, nil, fmt.Errorf("response exceeds audit limit")
	}
	return response.StatusCode, raw, nil
}

func CheckDeniedTool(ctx context.Context, endpoint, name string) error {
	if name != "audit_wait" {
		return fmt.Errorf("the deliberate denied tool must be audit_wait")
	}
	body := []byte(`{"jsonrpc":"2.0","id":87,"method":"tools/call","params":{"name":"audit_wait","arguments":{"milliseconds":1}}}`)
	status, raw, err := request(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	return ValidatePolicyDenial(status, raw)
}

func CheckAnonymousMCP(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Path != "/prod-audit/mcp" {
		return fmt.Errorf("anonymous MCP checks must target /prod-audit/mcp only")
	}
	body := []byte(`{"jsonrpc":"2.0","id":88,"method":"tools/call","params":{"name":"audit_echo","arguments":{"message":"synthetic-anonymous-probe"}}}`)
	status, _, err := request(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return fmt.Errorf("anonymous MCP request returned HTTP %d; expected authorization denial", status)
	}
	return nil
}

func CheckRegistry(ctx context.Context, target string) error {
	status, _, err := request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return fmt.Errorf("anonymous registry request returned HTTP %d; expected 401 or 403", status)
	}
	return nil
}

func CheckAuthEndpoint(ctx context.Context, issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil {
		return err
	}
	u.Path = "/.well-known/oauth-authorization-server" + strings.TrimRight(u.Path, "/")
	status, raw, err := request(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("authorization metadata returned HTTP %d", status)
	}
	return CheckAuthorizationMetadata(raw, issuer)
}
