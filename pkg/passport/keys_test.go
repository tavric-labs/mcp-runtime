package passport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHMACKeySignsAndVerifiesThroughTheKeyInterfaces(t *testing.T) {
	key, err := NewHMACKey([]byte("key-interface-secret"))
	if err != nil {
		t.Fatal(err)
	}
	var signer Signer = key
	var verifier Verifier = key

	now := time.Now()
	token, err := signer.Sign(Principal{Subject: "user-1", Role: RoleUser}, now, now.Add(time.Minute), []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(token, AudienceRuntime)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "user-1" || claims.Role != RoleUser {
		t.Fatalf("claims = %#v, want the signed principal", claims)
	}
	if _, err := verifier.Verify(token, AudienceAnalytics); err == nil {
		t.Fatal("token for one audience verified for another")
	}
}

func TestNewHMACKeyRejectsAnEmptySecret(t *testing.T) {
	for _, secret := range [][]byte{nil, {}} {
		if _, err := NewHMACKey(secret); err == nil {
			t.Fatalf("NewHMACKey(%q) succeeded, want an error", secret)
		}
	}
}

func TestAuthenticatorAcceptsAnInjectedVerifierWithoutASecret(t *testing.T) {
	secret := []byte("injected-verifier-secret")
	key, err := NewHMACKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := Sign(secret, Principal{Subject: "user-1", Role: RoleUser}, time.Minute, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}

	auth := Authenticator{Verifier: key, Audience: AudienceRuntime}
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

func TestExchangeHandlerUsesInjectedKeys(t *testing.T) {
	secret := []byte("injected-exchange-secret")
	key, err := NewHMACKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &lifecyclePrincipalStub{current: Principal{Subject: "user", Role: RoleUser}}
	source := exchangeSource(t, secret, Principal{Subject: "user", Role: RoleUser}, 15*time.Minute, AudiencePlatform)

	req := httptest.NewRequest(http.MethodGet, ExchangePath(AudienceRuntime), nil)
	req.Header.Set("Authorization", source)
	rec := httptest.NewRecorder()
	ExchangeHandler{
		Auth:   Authenticator{Verifier: key, Signer: key, Audience: AudiencePlatform, Principals: resolver},
		Target: AudienceRuntime,
	}.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	exchanged := rec.Header().Get("Authorization")
	if len(exchanged) <= len("Bearer ") {
		t.Fatalf("Authorization header = %q, want an exchanged bearer", exchanged)
	}
	claims, err := key.Verify(exchanged[len("Bearer "):], AudienceRuntime)
	if err != nil {
		t.Fatalf("exchanged token does not verify for the runtime audience: %v", err)
	}
	if claims.Subject != "user" {
		t.Fatalf("exchanged subject = %q, want user", claims.Subject)
	}
}
