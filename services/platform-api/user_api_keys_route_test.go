package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// User API keys are identity state, so platform-api owns the routes and stores
// the keys in Postgres. The wire format is the one runtime-api used to serve.
func userKeyServer() *apiServer {
	srv := &apiServer{
		userKeys: &fakeUserAPIKeyStore{
			ok:        true,
			principal: principal{Role: roleUser, Subject: "user-1", Namespace: "user-1", AuthType: "user_api_key"},
		},
	}
	testAuthenticator(srv)
	return srv
}

func serveUserKeyRequest(srv *apiServer, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("x-api-key", "mcpu_user")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUserAPIKeysAreServedByPlatformAPI(t *testing.T) {
	srv := userKeyServer()

	rec := serveUserKeyRequest(srv, http.MethodGet, "/api/v1/user/api-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"keys":[]}` {
		t.Fatalf("empty list body = %s, want {\"keys\":[]} so clients never see null", got)
	}

	rec = serveUserKeyRequest(srv, http.MethodPost, "/api/v1/user/api-keys", `{"name":"ci"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s, want 201", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"key"`, `"api_key"`, `"one_time_key"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Fatalf("create body %s lacks %s", rec.Body.String(), field)
		}
	}

	rec = serveUserKeyRequest(srv, http.MethodDelete, "/api/v1/user/api-keys/uk_1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	// DELETE is the only revoke form; there is no POST action alias.
	rec = serveUserKeyRequest(srv, http.MethodPost, "/api/v1/user/api-keys/uk_1/revoke", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST revoke status = %d body = %s, want 405", rec.Code, rec.Body.String())
	}
	rec = serveUserKeyRequest(srv, http.MethodGet, "/api/v1/user/api-keys/uk_1", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET item status = %d, want 405", rec.Code)
	}
}

func TestUserAPIKeysRequireAuthentication(t *testing.T) {
	srv := &apiServer{}
	testAuthenticator(srv)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user/api-keys", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
}
