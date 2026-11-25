package passport

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const Issuer = "mcp-runtime"

// Sign mints a token that expires ttl from now. Only a holder of a Signer,
// which is platform-api alone, can call it.
func Sign(signer Signer, p Principal, ttl time.Duration, audiences []string) (string, error) {
	if signer == nil {
		return "", errors.New("JWT signer is required")
	}
	if ttl <= 0 {
		return "", errors.New("JWT TTL must be positive")
	}
	now := time.Now().UTC()
	return signer.Sign(p, now, now.Add(ttl), audiences)
}

// buildClaims is shared by every signer so a token carries the same issuer,
// audience and lifetime claims whichever key signed it.
func buildClaims(p Principal, now, expires time.Time, audiences []string) Claims {
	claims := ClaimsFromPrincipal(p)
	claims.RegisteredClaims.Issuer = Issuer
	claims.RegisteredClaims.Audience = jwt.ClaimStrings(append([]string(nil), audiences...))
	claims.RegisteredClaims.IssuedAt = jwt.NewNumericDate(now)
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(expires)
	return claims
}
