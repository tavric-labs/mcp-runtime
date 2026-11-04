package passport

import "time"

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
