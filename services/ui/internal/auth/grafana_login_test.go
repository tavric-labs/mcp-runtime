package auth

import (
	"context"
	"mcp-runtime/pkg/topology"
	"mcp-ui/internal/session"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestAdminCheckGrafanaBrowserSignIn(t *testing.T) {
	store := session.NewStore(time.Now)
	admin, err := store.Create(context.Background(), session.Session{Principal: session.Principal{Role: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.Create(context.Background(), session.Session{Principal: session.Principal{Role: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, uri, method, accept, cookie, key string
		want                                   int
	}{
		{name: "signed out login", uri: "/grafana/login", method: "GET", accept: "text/html", want: 303},
		{name: "trace deep link", uri: "/grafana/explore?left=%7B%22trace%22%3A%22abc%22%7D", method: "GET", accept: "text/html,application/xhtml+xml", want: 303},
		{name: "expired session", uri: "/grafana/login", method: "GET", accept: "text/html", cookie: "expired", want: 303},
		{name: "member can switch account", uri: "/grafana/", method: "GET", accept: "text/html", cookie: member.ID, want: 303},
		{name: "admin session allowed", uri: "/grafana/login", method: "GET", accept: "text/html", cookie: admin.ID, want: 204},
		{name: "admin API key allowed", uri: "/grafana/login", method: "GET", accept: "text/html", key: "admin-key", want: 204},
		{name: "API client denied", uri: "/grafana/login", method: "GET", accept: "application/json", want: 401},
		{name: "HTML explicitly declined", uri: "/grafana/login", method: "GET", accept: "text/html;q=0.0,application/json", want: 401},
		{name: "HTML invalid quality", uri: "/grafana/login", method: "GET", accept: "text/html;q=bogus", want: 401},
		{name: "HEAD navigation", uri: "/grafana/login", method: "HEAD", accept: "text/html", want: 303},
		{name: "wildcard API client denied", uri: "/grafana/", method: "GET", accept: "*/*", want: 401},
		{name: "POST denied", uri: "/grafana/login", method: "POST", accept: "text/html", want: 401},
		{name: "API route denied", uri: "/grafana/api/datasources", method: "GET", accept: "text/html", want: 401},
		{name: "external URI denied", uri: "https://attacker.example/grafana/login", method: "GET", accept: "text/html", want: 401},
		{name: "network path denied", uri: "//attacker.example/grafana/login", method: "GET", accept: "text/html", want: 401},
		{name: "traversal denied", uri: "/grafana/%2e%2e/auth/login", method: "GET", accept: "text/html", want: 401},
		{name: "backslash denied", uri: "/grafana/\\attacker.example", method: "GET", accept: "text/html", want: 401},
		{name: "unrelated route denied", uri: "/auth/login", method: "GET", accept: "text/html", want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{Store: store}
			req := httptest.NewRequest("GET", "/auth/admin-check", nil)
			req.Header.Set("Accept", tc.accept)
			req.Header.Set("X-Forwarded-Method", tc.method)
			req.Header.Set("X-Forwarded-Uri", tc.uri)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: session.CookieName, Value: tc.cookie})
			}
			req.Header.Set("X-Api-Key", tc.key)
			response := httptest.NewRecorder()
			server.AdminCheck(nil, []string{"admin-key"}, false)(response, req)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tc.want, response.Body.String())
			}
			location := response.Header().Get("Location")
			if tc.want != 303 {
				if location != "" {
					t.Fatalf("unexpected redirect: %s", location)
				}
				return
			}
			want := "/#/signin?" + url.Values{"return_to": {tc.uri}}.Encode()
			if location != want {
				t.Fatalf("Location = %q, want %q", location, want)
			}
		})
	}
}

func TestAdminCheckGrafanaLoginUsesPublicRouteConfiguration(t *testing.T) {
	server := &Server{Store: session.NewStore(time.Now), PublicRoutes: topology.Routes{Platform: "/console", Grafana: "/observability"}}
	req := httptest.NewRequest("GET", "/auth/admin-check", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Method", "GET")
	req.Header.Set("X-Forwarded-Uri", "/observability/explore?orgId=1")
	response := httptest.NewRecorder()
	server.AdminCheck(nil, nil, false)(response, req)
	want := "/console/#/signin?" + url.Values{"return_to": {"/observability/explore?orgId=1"}}.Encode()
	if response.Code != 303 || response.Header().Get("Location") != want {
		t.Fatalf("status=%d Location=%q, want 303 %q", response.Code, response.Header().Get("Location"), want)
	}
}
