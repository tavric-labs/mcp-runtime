package auth

import (
	"context"
	"testing"

	"mcp-runtime/pkg/passport"
)

func TestRunPlatformAdminBootstrapRequiresASigningKey(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://example")
	t.Setenv(passport.EnvSigningKey, "")
	opened := false
	err := RunPlatformAdminBootstrap(context.Background(), func(context.Context, string, passport.Signer, passport.Verifier) (PasswordUserEnsurer, error) {
		opened = true
		return nil, nil
	})
	if err == nil {
		t.Fatal("bootstrap succeeded without a JWT signing key")
	}
	if opened {
		t.Fatal("store opened before the signing key was validated")
	}
}

func TestRunPlatformAdminBootstrapHandsTheStoreItsKeys(t *testing.T) {
	privatePEM, _, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTGRES_DSN", "postgres://example")
	t.Setenv(passport.EnvSigningKey, string(privatePEM))
	t.Setenv("PLATFORM_ADMIN_EMAIL", "")
	t.Setenv("PLATFORM_ADMIN_PASSWORD", "")

	var gotSigner passport.Signer
	var gotVerifier passport.Verifier
	err = RunPlatformAdminBootstrap(context.Background(), func(_ context.Context, dsn string, signer passport.Signer, verifier passport.Verifier) (PasswordUserEnsurer, error) {
		if dsn != "postgres://example" {
			t.Fatalf("dsn = %q", dsn)
		}
		gotSigner, gotVerifier = signer, verifier
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotSigner == nil || gotVerifier == nil {
		t.Fatalf("store opened with signer=%v verifier=%v, want both", gotSigner, gotVerifier)
	}
}
