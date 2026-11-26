package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/k8sclient"
)

// These tests lock the synchronous public contract before the #873 cutover.
// The asynchronous replacement intentionally changes acceptance and rollback.
func TestTeamCreateRollsBackIdentityAfterNamespaceProvisioningFailure(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "rollback succeeds"
		if cleanupFails {
			name = "rollback also fails"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
			var mu sync.Mutex
			var calls []string
			record := func(call string) { mu.Lock(); defer mu.Unlock(); calls = append(calls, call) }
			team := platformapi.Team{ID: "team-acme", Slug: "acme", Name: "Acme", Namespace: "mcp-team-acme"}
			identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/internal/identity/teams":
					record("create team")
					var body apikit.TeamCreateRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Slug != team.Slug || body.Name != team.Name || body.CreatedByUserID != "admin-1" {
						t.Errorf("unexpected team acceptance: %+v", body)
					}
					writeJSON(w, http.StatusCreated, team)
				case r.Method == http.MethodDelete && r.URL.Path == "/internal/identity/teams/acme":
					record("rollback team")
					if cleanupFails {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected identity request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer identity.Close()
			kube := kubernetesfake.NewSimpleClientset()
			kube.PrependReactor("create", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) {
				record("provision namespace")
				return true, nil, errors.New("fixture namespace admission denied")
			})
			server := &RuntimeServer{
				identity:   &platformapi.Client{BaseURL: identity.URL, Token: "fixture-internal-token", HTTP: identity.Client()},
				k8sClients: &k8sclient.Clients{Clientset: kube},
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/teams", strings.NewReader(`{"slug":"acme","name":"Acme"}`))
			request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
			response := httptest.NewRecorder()
			server.HandleRuntimeTeams(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s, want 500", response.Code, response.Body.String())
			}
			var envelope map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope["error"] != apikit.CodeInternalError || envelope["message"] != "failed to provision team namespace" {
				t.Fatalf("unexpected failure envelope: %+v", envelope)
			}
			mu.Lock()
			defer mu.Unlock()
			if want := []string{"create team", "provision namespace", "rollback team"}; !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v, want %v", calls, want)
			}
		})
	}
}

func TestTeamDeleteKeepsNamespaceAndReturnsNoContentWhenWatchCleanupFails(t *testing.T) {
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "required")
	t.Setenv("PLATFORM_TRAEFIK_NAMESPACE", "traefik")
	t.Setenv("PLATFORM_TRAEFIK_DEPLOYMENT", "traefik")
	var mu sync.Mutex
	var calls []string
	record := func(call string) { mu.Lock(); defer mu.Unlock(); calls = append(calls, call) }
	team := platformapi.Team{ID: "team-acme", Slug: "acme", Name: "Acme", Namespace: "mcp-team-acme"}
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/identity/teams/acme" {
			t.Errorf("unexpected identity path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			record("get team")
			writeJSON(w, http.StatusOK, team)
		case http.MethodDelete:
			record("delete team")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected identity method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer identity.Close()
	namespaceUID := types.UID("original-namespace")
	kube := kubernetesfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: team.Namespace, UID: namespaceUID}})
	kube.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		record("remove watch")
		return true, nil, errors.New("fixture Traefik API unavailable")
	})
	server := &RuntimeServer{
		identity:   &platformapi.Client{BaseURL: identity.URL, Token: "fixture-internal-token", HTTP: identity.Client()},
		k8sClients: &k8sclient.Clients{Clientset: kube},
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/runtime/teams/acme", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
	response := httptest.NewRecorder()
	server.HandleRuntimeTeamItemPath(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s, want empty 204", response.Code, response.Body.String())
	}
	remaining, err := kube.CoreV1().Namespaces().Get(context.Background(), team.Namespace, metav1.GetOptions{})
	if err != nil || remaining.UID != namespaceUID {
		t.Fatalf("team namespace was not preserved: namespace=%v err=%v", remaining, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("team deletion unexpectedly deleted a Kubernetes resource: %s", action.GetResource().Resource)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"get team", "delete team", "remove watch"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
}
