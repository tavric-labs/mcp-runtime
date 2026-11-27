package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/openapi"
)

func TestDirectoryRoutesRequirePlatformAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/teams"}, {http.MethodPost, "/api/v1/teams"},
		{http.MethodGet, "/api/v1/teams/team-a"}, {http.MethodDelete, "/api/v1/teams/team-a"},
		{http.MethodGet, "/api/v1/operations/operation-id"},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != 401 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if err := openapi.ValidateResponse(doc, tt.method, tt.path, w.Code, w.Body.Bytes(), "application/json"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
