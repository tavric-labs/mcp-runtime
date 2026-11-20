package runtimeapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
)

const egressTestNamespace = "user-1"

func egressTestOwner() principal {
	return principal{
		Role:              roleUser,
		Subject:           "user-1",
		Namespace:         egressTestNamespace,
		AllowedNamespaces: []string{egressTestNamespace},
	}
}

func applyEgressTestServer(t *testing.T, server *RuntimeServer, p principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/runtime/servers", bytes.NewReader([]byte(body)))
	request = request.WithContext(withPrincipal(request.Context(), p))
	recorder := httptest.NewRecorder()
	server.HandleRuntimeServers(recorder, request)
	return recorder
}

func approvedEgressServer(hosts ...string) *mcpv1alpha1.MCPServer {
	existing := ownedTestMCPServer("demo", egressTestNamespace, "user-1")
	existing.Annotations = map[string]string{mcpaccess.EgressApprovedAnnotation: "true"}
	existing.Spec.Egress = &mcpv1alpha1.EgressConfig{Hosts: hosts}
	return existing
}

func currentEgressTestServer(t *testing.T, server *RuntimeServer) *mcpv1alpha1.MCPServer {
	t.Helper()
	current, err := server.controlPlane().GetServer(context.Background(), egressTestNamespace, "demo")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	return current
}

// A publisher must never be able to approve their own egress: the apply API
// has no annotation input, so an annotation smuggled into the request body is
// ignored.
func TestRuntimeServerApplyIgnoresRequestedEgressApproval(t *testing.T) {
	t.Setenv(envPushCooldown, "0s")
	server := newRuntimeServerWithMCPServers(t)

	recorder := applyEgressTestServer(t, server, egressTestOwner(), `{
		"name": "demo",
		"namespace": "user-1",
		"annotations": {"mcpruntime.org/egress-approved": "true"},
		"metadata": {"annotations": {"mcpruntime.org/egress-approved": "true"}},
		"spec": {"image":"registry.example.com/user-1/demo","egress":{"hosts":["www.googleapis.com"]}}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	current := currentEgressTestServer(t, server)
	if current.Spec.Egress == nil || len(current.Spec.Egress.Hosts) != 1 || current.Spec.Egress.Hosts[0] != "www.googleapis.com" {
		t.Fatalf("persisted egress = %+v, want the declared host", current.Spec.Egress)
	}
	if got := current.Annotations[mcpaccess.EgressApprovedAnnotation]; got != "" {
		t.Fatalf("approval annotation = %q, want none: only an admin may approve", got)
	}
}

func TestRuntimeServerApplyDropsApprovalWhenEgressChanges(t *testing.T) {
	t.Setenv(envPushCooldown, "0s")
	for name, p := range map[string]principal{
		"owner": egressTestOwner(),
		"admin": {Role: roleAdmin, Subject: "admin-1"},
	} {
		t.Run(name, func(t *testing.T) {
			server := newRuntimeServerWithMCPServers(t, approvedEgressServer("www.googleapis.com"))

			recorder := applyEgressTestServer(t, server, p, `{
				"name": "demo",
				"namespace": "user-1",
				"update": true,
				"spec": {"image":"registry.example.com/user-1/demo","egress":{"hosts":["www.googleapis.com","evil.example.net"]}}
			}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			if got := currentEgressTestServer(t, server).Annotations[mcpaccess.EgressApprovedAnnotation]; got != "" {
				t.Fatalf("approval annotation = %q after the declaration changed, want it dropped", got)
			}
		})
	}
}

func TestRuntimeServerApplyDropsApprovalWhenEgressRemovedAndReadded(t *testing.T) {
	t.Setenv(envPushCooldown, "0s")
	server := newRuntimeServerWithMCPServers(t, approvedEgressServer("www.googleapis.com"))

	recorder := applyEgressTestServer(t, server, egressTestOwner(), `{
		"name": "demo", "namespace": "user-1", "update": true,
		"spec": {"image":"registry.example.com/user-1/demo"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got := currentEgressTestServer(t, server).Annotations[mcpaccess.EgressApprovedAnnotation]; got != "" {
		t.Fatalf("approval annotation = %q after egress was removed, want it dropped", got)
	}
}

func TestRuntimeServerApplyKeepsApprovalWhenEgressUnchanged(t *testing.T) {
	t.Setenv(envPushCooldown, "0s")
	server := newRuntimeServerWithMCPServers(t, approvedEgressServer("www.googleapis.com"))

	recorder := applyEgressTestServer(t, server, egressTestOwner(), `{
		"name": "demo", "namespace": "user-1", "update": true,
		"spec": {"image":"registry.example.com/user-1/demo:v2","egress":{"hosts":["WWW.GoogleAPIs.com"],"ports":[443]}}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	current := currentEgressTestServer(t, server)
	if got := current.Annotations[mcpaccess.EgressApprovedAnnotation]; got != "true" {
		t.Fatalf("approval annotation = %q, want an unchanged declaration to stay approved", got)
	}
	if len(current.Spec.Egress.Hosts) != 1 || current.Spec.Egress.Hosts[0] != "www.googleapis.com" {
		t.Fatalf("persisted egress hosts = %v, want normalized lowercase host", current.Spec.Egress.Hosts)
	}
}

func TestRuntimeServerApplyRejectsInvalidEgress(t *testing.T) {
	t.Setenv(envPushCooldown, "0s")
	for name, egress := range map[string]string{
		"wildcard":   `{"hosts":["*.googleapis.com"]}`,
		"ip literal": `{"hosts":["169.254.169.254"]}`,
		"no hosts":   `{"hosts":[]}`,
		"bad port":   `{"hosts":["example.com"],"ports":[70000]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newRuntimeServerWithMCPServers(t)
			recorder := applyEgressTestServer(t, server, egressTestOwner(), `{
				"name": "demo", "namespace": "user-1",
				"spec": {"image":"registry.example.com/user-1/demo","egress":`+egress+`}
			}`)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want 400", recorder.Code, recorder.Body.String())
			}
		})
	}
}
