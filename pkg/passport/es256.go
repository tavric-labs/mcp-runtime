package passport

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const (
	// EnvSigningKey names the PEM-encoded ES256 private key. Only platform-api
	// receives it.
	EnvSigningKey = "JWT_SIGNING_KEY"
	// EnvVerifyKeys names one or more PEM-encoded ES256 public keys. Services
	// that only verify tokens receive just these. During rotation it holds the
	// outgoing and incoming keys together.
	EnvVerifyKeys = "JWT_VERIFY_KEY"
)

// keyID derives a stable identifier from the public key so signer and
// verifier agree on it without any extra configuration.
func keyID(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// GenerateES256KeyPair creates a P-256 key pair as PKCS#8 and PKIX PEM.
func GenerateES256KeyPair() (privatePEM, publicPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), nil
}

// ES256Signer signs platform tokens with a P-256 private key.
type ES256Signer struct {
	key *ecdsa.PrivateKey
	kid string
}

// NewES256Signer parses a PKCS#8 or SEC1 PEM private key. Only P-256 keys are
// valid for ES256.
func NewES256Signer(privatePEM []byte) (*ES256Signer, error) {
	block, _ := pem.Decode(privatePEM)
	if block == nil {
		return nil, errors.New("JWT signing key is not valid PEM")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse JWT signing key: %w", err)
		}
		ec, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("JWT signing key must be an ECDSA P-256 key")
		}
		key = ec
	case "EC PRIVATE KEY":
		parsed, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse JWT signing key: %w", err)
		}
		key = parsed
	default:
		return nil, fmt.Errorf("unsupported JWT signing key type %q", block.Type)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("JWT signing key must use curve P-256")
	}
	kid, err := keyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &ES256Signer{key: key, kid: kid}, nil
}

func (s *ES256Signer) Sign(p Principal, now, expires time.Time, audiences []string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, buildClaims(p, now, expires, audiences))
	token.Header["kid"] = s.kid
	return token.SignedString(s.key)
}

// PublicKeyPEM returns the matching verification key.
func (s *ES256Signer) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// Verifier returns a verifier that trusts only this signer's own public key.
func (s *ES256Signer) Verifier() *ES256Verifier {
	return &ES256Verifier{keys: map[string]*ecdsa.PublicKey{s.kid: &s.key.PublicKey}}
}

// ES256Verifier verifies platform tokens against a set of public keys chosen by
// the token's kid header. It holds no private key and cannot sign.
type ES256Verifier struct {
	keys map[string]*ecdsa.PublicKey
}

// NewES256Verifier parses every PUBLIC KEY block in the given PEM inputs. A
// private key or any other block type is an error, so a key that should have
// stayed secret is never silently accepted as a verification key.
func NewES256Verifier(publicPEMs ...[]byte) (*ES256Verifier, error) {
	keys := map[string]*ecdsa.PublicKey{}
	for _, data := range publicPEMs {
		rest := data
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				if len(strings.TrimSpace(string(rest))) > 0 {
					return nil, errors.New("JWT verification key is not valid PEM")
				}
				break
			}
			if block.Type != "PUBLIC KEY" {
				return nil, fmt.Errorf("JWT verification keys must be PUBLIC KEY blocks, got %q", block.Type)
			}
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse JWT verification key: %w", err)
			}
			pub, ok := parsed.(*ecdsa.PublicKey)
			if !ok || pub.Curve != elliptic.P256() {
				return nil, errors.New("JWT verification key must be an ECDSA P-256 key")
			}
			kid, err := keyID(pub)
			if err != nil {
				return nil, err
			}
			keys[kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one JWT verification key is required")
	}
	return &ES256Verifier{keys: keys}, nil
}

// Verify accepts only ES256 tokens whose kid names a known key. The algorithm
// is pinned: the token header is attacker controlled, so none and every HMAC
// variant are rejected before any key is looked up.
func (v *ES256Verifier) Verify(token, expectedAudience string) (Claims, error) {
	if strings.TrimSpace(expectedAudience) == "" {
		return Claims{}, errors.New("expected audience is required")
	}
	var claims Claims
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}))
	parsed, err := parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodES256 {
			return nil, fmt.Errorf("unexpected signing method %s", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := v.keys[kid]
		if kid == "" || !ok {
			return nil, errors.New("unknown JWT key id")
		}
		return key, nil
	})
	if err != nil {
		return Claims{}, err
	}
	if !parsed.Valid {
		return Claims{}, errors.New("invalid JWT")
	}
	return validateClaims(claims, expectedAudience)
}

// PlatformKeysFromEnv builds what platform-api needs: the signer from
// JWT_SIGNING_KEY, and a verifier trusting its own public key plus any extra
// public keys in JWT_VERIFY_KEY, which lets old tokens survive a rotation.
func PlatformKeysFromEnv() (Signer, Verifier, error) {
	raw := strings.TrimSpace(os.Getenv(EnvSigningKey))
	if raw == "" {
		return nil, nil, fmt.Errorf("%s is required", EnvSigningKey)
	}
	signer, err := NewES256Signer([]byte(raw))
	if err != nil {
		return nil, nil, err
	}
	own, err := signer.PublicKeyPEM()
	if err != nil {
		return nil, nil, err
	}
	inputs := [][]byte{own}
	if extra := strings.TrimSpace(os.Getenv(EnvVerifyKeys)); extra != "" {
		inputs = append(inputs, []byte(extra))
	}
	verifier, err := NewES256Verifier(inputs...)
	if err != nil {
		return nil, nil, err
	}
	return signer, verifier, nil
}

// VerifierFromEnv builds the verifier a backend service uses. It reads only
// public keys, so a compromised backend cannot mint platform tokens.
func VerifierFromEnv() (Verifier, error) {
	raw := strings.TrimSpace(os.Getenv(EnvVerifyKeys))
	if raw == "" {
		return nil, fmt.Errorf("%s is required", EnvVerifyKeys)
	}
	return NewES256Verifier([]byte(raw))
}

// EnsureVerifyKey returns the verification key set a deployment should publish
// for a signing key: the existing set, plus the signing key's public key when
// the set does not hold it yet. Old keys stay until an operator removes them, so
// tokens signed before a rotation keep verifying. An existing set that is not
// valid, or that holds a private key, is an error and is never overwritten.
func EnsureVerifyKey(signingPEM, existingVerifyPEM []byte) ([]byte, error) {
	signer, err := NewES256Signer(signingPEM)
	if err != nil {
		return nil, err
	}
	own, err := signer.PublicKeyPEM()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(existingVerifyPEM)) == 0 {
		return own, nil
	}
	verifier, err := NewES256Verifier(existingVerifyPEM)
	if err != nil {
		return nil, fmt.Errorf("existing %s: %w", EnvVerifyKeys, err)
	}
	if _, ok := verifier.keys[signer.kid]; ok {
		return existingVerifyPEM, nil
	}
	merged := append(bytes.TrimRight(existingVerifyPEM, "\n"), '\n')
	return append(merged, own...), nil
}
