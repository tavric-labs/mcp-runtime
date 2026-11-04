package passport

import (
	"errors"
	"time"
)

// Signer mints compact JWTs for a principal. Only platform-api holds one.
type Signer interface {
	Sign(p Principal, now, expires time.Time, audiences []string) (string, error)
}

// Verifier validates a compact JWT for exactly one expected audience. A service
// that only verifies tokens must not be able to mint them, so verification and
// signing are separate interfaces.
type Verifier interface {
	Verify(token, expectedAudience string) (Claims, error)
}

// HMACKey signs and verifies with one shared HS256 secret.
type HMACKey struct {
	secret []byte
}

// NewHMACKey wraps a shared secret as both a Signer and a Verifier.
func NewHMACKey(secret []byte) (HMACKey, error) {
	if len(secret) == 0 {
		return HMACKey{}, errors.New("JWT secret is required")
	}
	return HMACKey{secret: append([]byte(nil), secret...)}, nil
}

func (k HMACKey) Sign(p Principal, now, expires time.Time, audiences []string) (string, error) {
	return signUntil(k.secret, p, now, expires, audiences)
}

func (k HMACKey) Verify(token, expectedAudience string) (Claims, error) {
	return Verify(k.secret, token, expectedAudience)
}

// verifier returns the configured Verifier, or nil when the authenticator has
// no key material and therefore does not accept platform JWTs.
func (a Authenticator) verifier() Verifier {
	if a.Verifier != nil {
		return a.Verifier
	}
	if len(a.Secret) == 0 {
		return nil
	}
	return HMACKey{secret: a.Secret}
}

// signer returns the configured Signer, or nil when the authenticator cannot
// mint tokens.
func (a Authenticator) signer() Signer {
	if a.Signer != nil {
		return a.Signer
	}
	if len(a.Secret) == 0 {
		return nil
	}
	return HMACKey{secret: a.Secret}
}
