package passport

import (
	"errors"
	"strings"
)

// validateClaims applies the checks every platform token must pass after its
// signature is verified.
func validateClaims(claims Claims, expectedAudience string) (Claims, error) {
	if claims.Issuer != Issuer {
		return Claims{}, errors.New("invalid JWT issuer")
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != expectedAudience {
		return Claims{}, errors.New("invalid JWT audience")
	}
	if claims.ExpiresAt == nil {
		return Claims{}, errors.New("JWT expiration is required")
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Claims{}, errors.New("JWT subject is required")
	}
	return claims, nil
}
