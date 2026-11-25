package passport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthenticatorAcceptsAnInjectedVerifierWithoutASigner(t *testing.T) {
	signer, verifier := testKeys(t)
	token, err := Sign(signer, Principal{Subject: "user-1", Role: RoleUser}, time.Minute, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}

	auth := Authenticator{Verifier: verifier, Audience: AudienceRuntime}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/teams", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got, ok, err := auth.AuthenticateRequest(req)
	if err != nil || !ok {
		t.Fatalf("AuthenticateRequest() ok=%v err=%v", ok, err)
	}
	if got.UserID() != "user-1" {
		t.Fatalf("principal = %#v, want user-1", got)
	}
}

func TestAuthenticatorWithoutAVerifierRejectsPlatformJWTs(t *testing.T) {
	signer, _ := testKeys(t)
	token, err := Sign(signer, Principal{Subject: "user-1", Role: RoleUser}, time.Minute, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if _, ok, _ := (Authenticator{Audience: AudienceRuntime}).AuthenticateRequest(req); ok {
		t.Fatal("authenticator with no verifier accepted a JWT")
	}
}

func TestSignRequiresASignerAndAPositiveTTL(t *testing.T) {
	if _, err := Sign(nil, Principal{Subject: "user"}, time.Minute, []string{AudienceRuntime}); err == nil {
		t.Fatal("Sign with no signer succeeded")
	}
	signer, _ := testKeys(t)
	if _, err := Sign(signer, Principal{Subject: "user"}, 0, []string{AudienceRuntime}); err == nil {
		t.Fatal("Sign with zero TTL succeeded")
	}
}

func TestExchangeHandlerIsUnavailableWithoutASigner(t *testing.T) {
	_, verifier := testKeys(t)
	resolver := &lifecyclePrincipalStub{current: Principal{Subject: "user", Role: RoleUser}}
	req := httptest.NewRequest(http.MethodGet, ExchangePath(AudienceRuntime), nil)
	rec := httptest.NewRecorder()
	ExchangeHandler{
		Auth:   Authenticator{Verifier: verifier, Audience: AudiencePlatform, Principals: resolver},
		Target: AudienceRuntime,
	}.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestExchangeHandlerMintsOnlyForTheFixedTargetAudience(t *testing.T) {
	signer, verifier := testKeys(t)
	resolver := &lifecyclePrincipalStub{current: Principal{Subject: "user", Role: RoleUser}}
	source := exchangeSource(t, signer, Principal{Subject: "user", Role: RoleUser}, 15*time.Minute, AudiencePlatform)

	req := httptest.NewRequest(http.MethodGet, ExchangePath(AudienceRuntime), nil)
	req.Header.Set("Authorization", source)
	rec := httptest.NewRecorder()
	ExchangeHandler{
		Auth:   Authenticator{Verifier: verifier, Signer: signer, Audience: AudiencePlatform, Principals: resolver},
		Target: AudienceRuntime,
	}.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	exchanged := rec.Header().Get("Authorization")
	if len(exchanged) <= len("Bearer ") {
		t.Fatalf("Authorization header = %q, want an exchanged bearer", exchanged)
	}
	if _, err := verifier.Verify(exchanged[len("Bearer "):], AudienceRuntime); err != nil {
		t.Fatalf("exchanged token does not verify for the runtime audience: %v", err)
	}
	if _, err := verifier.Verify(exchanged[len("Bearer "):], AudienceAnalytics); err == nil {
		t.Fatal("exchanged runtime token verified for the analytics audience")
	}
}
