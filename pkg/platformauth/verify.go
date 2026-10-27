package platformauth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v4"
)

func Verify(secret []byte, token, expectedAudience string) (Claims, error) {
	if len(secret) == 0 {
		return Claims{}, errors.New("JWT secret is required")
	}
	if strings.TrimSpace(expectedAudience) == "" {
		return Claims{}, errors.New("expected audience is required")
	}
	var claims Claims
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	parsed, err := parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %s", t.Method.Alg())
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	if !parsed.Valid {
		return Claims{}, errors.New("invalid JWT")
	}
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

// JWTSecretFromEnv reads the shared platform JWT signing secret from JWT_SECRET.
func JWTSecretFromEnv() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if secret == "" {
		return nil, errors.New("JWT_SECRET is required")
	}
	return []byte(secret), nil
}
