package platformstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/passport/passporttest"
)

func TestEnsureTeamPasswordUserTrimsBeforeLengthCheck(t *testing.T) {
	store := NewForTest(nil, nil)
	_, err := store.EnsureTeamPasswordUser(context.Background(), "member@example.com", " 12345678901 ")
	if err == nil {
		t.Fatal("expected short password error")
	}
	if !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("expected password length error, got %v", err)
	}
}

func TestLoginAccessTokenIsPlatformOnly(t *testing.T) {
	signer, verifier := passporttest.NewKeys(t)
	store := NewForTest(signer, verifier)
	token, err := store.CreateAccessToken(User{ID: "user", Email: "user@example.com", Role: RoleUser}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, passport.AudiencePlatform); err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{passport.AudienceRuntime, passport.AudienceAnalytics} {
		if _, err := verifier.Verify(token, audience); err == nil {
			t.Fatalf("login token accepted directly at %s", audience)
		}
	}
}
