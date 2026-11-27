package platformapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/openapi"
	"os"
)

func TestTeamAcceptanceWaitContract(t *testing.T) {
	spec, err := os.ReadFile("../../../services/platform-api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi.Load(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, noWait := range []bool{false, true} {
		t.Run(fmt.Sprintf("noWait=%v", noWait), func(t *testing.T) {
			var calls []string
			client := &PlatformClient{baseURL: "https://platform.example.com", token: "fixture", apiPrefix: "/api/v1", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				template := r.URL.Path
				if template == "/api/v1/operations/operation-id" {
					template = "/api/v1/operations/{id}"
				}
				if template == "/api/v1/teams/core" {
					template = "/api/v1/teams/{slug}"
				}
				path := doc.Paths.Find(template)
				if path == nil || path.GetOperation(r.Method) == nil {
					t.Fatalf("consumer call absent from provider contract: %s %s", r.Method, r.URL.Path)
				}
				var body string
				switch r.Method + " " + r.URL.Path {
				case "POST /api/v1/teams":
					if r.Header.Get("Idempotency-Key") != "lost-response-key" {
						t.Fatalf("missing caller replay key: %q", r.Header.Get("Idempotency-Key"))
					}
					return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(`{"team":{"id":"team-id","slug":"core","namespace":"mcp-team-core"},"operation":{"id":"operation-id","kind":"team.create","resource_id":"team-id","desired_revision_id":"revision-id","state":"Pending"}}`))}, nil
				case "GET /api/v1/operations/operation-id":
					body = `{"team":{"id":"team-id","slug":"core","namespace":"mcp-team-core"},"operation":{"id":"operation-id","kind":"team.create","resource_id":"team-id","desired_revision_id":"revision-id","state":"Succeeded"}}`
				case "GET /api/v1/teams/core":
					body = `{"team":{"id":"team-id","slug":"core","namespace":"mcp-team-core"},"status":{"desired_revision_id":"revision-id","applied_revision_id":"revision-id","observed_revision_id":"revision-id","health":"Ready","reason":"NamespaceReady"}}`
				default:
					t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			accepted, err := client.AcceptTeamCreate(ctx, "core", "Core", MutationOptions{NoWait: noWait, IdempotencyKey: "lost-response-key"})
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 3
			wantState := "Succeeded"
			if noWait {
				wantCalls = 1
				wantState = "Pending"
			}
			if len(calls) != wantCalls || accepted.Operation.ID != "operation-id" || accepted.Operation.State != wantState {
				t.Fatalf("acceptance=%+v calls=%v", accepted, calls)
			}
		})
	}
}

func TestTeamAcceptancePreservesTerminalResultWithoutHealthPolling(t *testing.T) {
	var paths []string
	client := &PlatformClient{baseURL: "https://platform.example.com", token: "fixture", apiPrefix: "/api/v1", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		body := `{"team":{"id":"team-id","slug":"core"},"operation":{"id":"operation-id","resource_id":"team-id","desired_revision_id":"revision-id","state":"Pending"}}`
		status := 202
		if r.Method == http.MethodGet {
			if r.URL.Path != "/api/v1/operations/operation-id" {
				t.Fatalf("polled health after terminal execution: %s", r.URL.Path)
			}
			body = `{"operation":{"id":"operation-id","state":"Failed","error_class":"invalid_config"}}`
			status = 200
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	accepted, err := client.AcceptTeamCreate(t.Context(), "core", "Core", MutationOptions{})
	if err == nil || !strings.Contains(err.Error(), "operation-id") || accepted.Operation.State != "Failed" || len(paths) != 2 {
		t.Fatalf("accepted=%+v err=%v paths=%v", accepted, err, paths)
	}
}
