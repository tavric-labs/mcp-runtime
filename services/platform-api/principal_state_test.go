package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-runtime/pkg/passport"
)

func TestProductionNoDatabasePrincipalResolverFailsClosedForHumanBearer(t *testing.T) {
	secret := []byte("lifecycle-test-secret")
	token, err := passport.Sign(secret, passport.Principal{Subject: "account", Role: passport.RoleAdmin}, time.Hour, []string{passport.AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	a := passport.Authenticator{Secret: secret, Audience: passport.AudiencePlatform, Principals: storeAPIKeyResolver{}, ServiceAPIKeys: map[string]struct{}{"service": {}}, AdminAPIKeys: map[string]struct{}{"service": {}}}
	r := httptest.NewRequest(http.MethodGet, "/private", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, ok, err := a.AuthenticateRequest(r); ok || err == nil {
		t.Fatalf("no-DB human token accepted: ok=%t err=%v", ok, err)
	}
	r.Header.Del("Authorization")
	r.Header.Set("x-api-key", "service")
	p, ok, err := a.AuthenticateRequest(r)
	if err != nil || !ok || p.UserID() != "" || !p.IsService {
		t.Fatalf("service identity changed: principal=%+v ok=%t err=%v", p, ok, err)
	}
}
