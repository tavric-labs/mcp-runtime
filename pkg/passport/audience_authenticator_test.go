package passport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A backend service accepts the short-lived token platform-api minted for its
// audience after re-reading the account, so it must not call platform-api again
// for every request. API keys are the exception: a revoked key must fail at once.
func TestAudienceAuthenticatorVerifiesExchangeTokensLocally(t *testing.T) {
	var platformCalls atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer platform.Close()

	secret := []byte("test-secret")
	auth := NewAudienceAuthenticator(AudienceConfig{
		Audience:       AudienceRuntime,
		Secret:         secret,
		PlatformAPIURL: platform.URL,
		InternalToken:  "internal-token",
	})

	exchanged := Principal{
		Subject: "user-1",
		Email:   "user@example.com",
		Role:    RoleUser,
		Teams:   []PrincipalTeam{{ID: "team-1", Slug: "team-one", Role: "member"}},
	}
	token, err := Sign(secret, exchanged, ExchangeTTL, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/teams", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got, ok, err := auth.AuthenticateRequest(req)
	if err != nil || !ok {
		t.Fatalf("AuthenticateRequest() ok=%v err=%v", ok, err)
	}
	if got.UserID() != "user-1" || got.Role != RoleUser || len(got.Teams) != 1 || got.Teams[0].Slug != "team-one" {
		t.Fatalf("principal = %#v, want the claims minted by platform-api", got)
	}
	if calls := platformCalls.Load(); calls != 0 {
		t.Fatalf("platform-api was called %d times to authenticate an exchange token, want 0", calls)
	}

	expired, err := signUntil(secret, exchanged, time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute), []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/teams", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	if _, ok, _ := auth.AuthenticateRequest(req); ok {
		t.Fatal("expired exchange token authenticated")
	}

	wrongAudience, err := Sign(secret, exchanged, ExchangeTTL, []string{AudienceAnalytics})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/teams", nil)
	req.Header.Set("Authorization", "Bearer "+wrongAudience)
	if _, ok, _ := auth.AuthenticateRequest(req); ok {
		t.Fatal("token for another audience authenticated")
	}
}

func TestAudienceAuthenticatorStillResolvesAPIKeysRemotely(t *testing.T) {
	var platformCalls atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "principal": Principal{Subject: "user-1", Role: RoleUser}})
	}))
	defer platform.Close()

	auth := NewAudienceAuthenticator(AudienceConfig{
		Audience:       AudienceAnalytics,
		Secret:         []byte("test-secret"),
		PlatformAPIURL: platform.URL,
		InternalToken:  "internal-token",
	})
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/usage", nil)
		req.Header.Set("x-api-key", "user-key")
		if _, ok, err := auth.AuthenticateRequest(req); err != nil || !ok {
			t.Fatalf("AuthenticateRequest() ok=%v err=%v", ok, err)
		}
	}
	if calls := platformCalls.Load(); calls != 2 {
		t.Fatalf("platform-api resolved the API key %d times, want 2 so a revoked key fails immediately", calls)
	}
}
