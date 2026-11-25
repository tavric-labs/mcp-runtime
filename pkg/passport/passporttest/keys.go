// Package passporttest gives tests across the repository ES256 keys and tokens
// without each one re-deriving how platform tokens are signed.
package passporttest

import (
	"testing"
	"time"

	"mcp-runtime/pkg/passport"
)

// NewKeys returns a fresh signer and a verifier that trusts only its key.
func NewKeys(t testing.TB) (*passport.ES256Signer, *passport.ES256Verifier) {
	t.Helper()
	privatePEM, _, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := passport.NewES256Signer(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.Verifier()
}

// Token signs a platform token for the given audiences.
func Token(t testing.TB, signer passport.Signer, p passport.Principal, ttl time.Duration, audiences ...string) string {
	t.Helper()
	token, err := passport.Sign(signer, p, ttl, audiences)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
