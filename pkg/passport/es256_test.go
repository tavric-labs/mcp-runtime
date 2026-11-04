package passport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func newTestKeyPair(t *testing.T) (privatePEM, publicPEM []byte) {
	t.Helper()
	privatePEM, publicPEM, err := GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return privatePEM, publicPEM
}

// testKeys returns a fresh signer and the verifier that trusts only its key.
func testKeys(t testing.TB) (*ES256Signer, *ES256Verifier) {
	t.Helper()
	privatePEM, _, err := GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewES256Signer(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.Verifier()
}

func testVerifier(t testing.TB) *ES256Verifier {
	t.Helper()
	_, verifier := testKeys(t)
	return verifier
}

// signRawClaims signs arbitrary claims the way a real token is signed, so a
// test can forge tokens that are validly signed but carry bad claims.
func signRawClaims(t testing.TB, signer *ES256Signer, claims Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = signer.kid
	signed, err := token.SignedString(signer.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func signTestToken(t *testing.T, signer Signer, audience string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	token, err := signer.Sign(Principal{Subject: "user-1", Role: RoleUser}, now, now.Add(ttl), []string{audience})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestES256TokenVerifiesWithOnlyThePublicKey(t *testing.T) {
	privatePEM, publicPEM := newTestKeyPair(t)
	signer, err := NewES256Signer(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewES256Verifier(publicPEM)
	if err != nil {
		t.Fatal(err)
	}

	token := signTestToken(t, signer, AudienceRuntime, time.Minute)
	claims, err := verifier.Verify(token, AudienceRuntime)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "user-1" || claims.Role != RoleUser {
		t.Fatalf("claims = %#v, want the signed principal", claims)
	}
	if _, err := verifier.Verify(token, AudienceAnalytics); err == nil {
		t.Fatal("token for one audience verified for another")
	}
}

// A verifier-only service holds the public key, which is not secret. It must
// not be possible to turn that into a token the platform accepts.
func TestES256VerifierRejectsHMACTokensSignedWithThePublicKey(t *testing.T) {
	_, publicPEM := newTestKeyPair(t)
	verifier, err := NewES256Verifier(publicPEM)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(publicPEM)
	for name, secret := range map[string][]byte{"pem": publicPEM, "der": block.Bytes} {
		claims := ClaimsFromPrincipal(Principal{Subject: "attacker", Role: RoleAdmin, IsService: true})
		claims.RegisteredClaims.Issuer = Issuer
		claims.RegisteredClaims.Audience = jwt.ClaimStrings{AudiencePlatform}
		claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
		forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(forged, AudiencePlatform); err == nil {
			t.Fatalf("HS256 token signed with the public key (%s) was accepted", name)
		}
	}
}

func TestES256VerifierRejectsUnsignedAndTamperedTokens(t *testing.T) {
	privatePEM, publicPEM := newTestKeyPair(t)
	signer, _ := NewES256Signer(privatePEM)
	verifier, _ := NewES256Verifier(publicPEM)

	token := signTestToken(t, signer, AudiencePlatform, time.Minute)
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))
	if _, err := verifier.Verify(tampered, AudiencePlatform); err == nil {
		t.Fatal("token with a tampered signature was accepted")
	}

	claims := ClaimsFromPrincipal(Principal{Subject: "user-1", Role: RoleAdmin})
	claims.RegisteredClaims.Issuer = Issuer
	claims.RegisteredClaims.Audience = jwt.ClaimStrings{AudiencePlatform}
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(none, AudiencePlatform); err == nil {
		t.Fatal("unsigned token was accepted")
	}
}

func TestES256VerifierRejectsExpiredTokens(t *testing.T) {
	privatePEM, publicPEM := newTestKeyPair(t)
	signer, _ := NewES256Signer(privatePEM)
	verifier, _ := NewES256Verifier(publicPEM)
	now := time.Now()
	expired, err := signer.Sign(Principal{Subject: "user-1", Role: RoleUser}, now.Add(-2*time.Minute), now.Add(-time.Minute), []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(expired, AudienceRuntime); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestES256VerifierRejectsUnknownOrMissingKeyID(t *testing.T) {
	privatePEM, _ := newTestKeyPair(t)
	_, otherPublic := newTestKeyPair(t)
	signer, _ := NewES256Signer(privatePEM)
	verifier, _ := NewES256Verifier(otherPublic)

	if _, err := verifier.Verify(signTestToken(t, signer, AudienceRuntime, time.Minute), AudienceRuntime); err == nil {
		t.Fatal("token signed by an unknown key was accepted")
	}

	// A token signed by the right key but without a kid must not fall back to
	// "try every key".
	pemKey, _ := NewES256Signer(privatePEM)
	claims := ClaimsFromPrincipal(Principal{Subject: "user-1", Role: RoleUser})
	claims.RegisteredClaims.Issuer = Issuer
	claims.RegisteredClaims.Audience = jwt.ClaimStrings{AudienceRuntime}
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	noKid, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(pemKey.key)
	if err != nil {
		t.Fatal(err)
	}
	ownVerifier := pemKey.Verifier()
	if _, err := ownVerifier.Verify(noKid, AudienceRuntime); err == nil {
		t.Fatal("token without a kid was accepted")
	}
}

func TestES256VerifierAcceptsEveryKeyInTheSetForRotation(t *testing.T) {
	oldPrivate, oldPublic := newTestKeyPair(t)
	newPrivate, newPublic := newTestKeyPair(t)
	oldSigner, _ := NewES256Signer(oldPrivate)
	newSigner, _ := NewES256Signer(newPrivate)
	verifier, err := NewES256Verifier(append(append([]byte(nil), oldPublic...), newPublic...))
	if err != nil {
		t.Fatal(err)
	}
	for name, signer := range map[string]Signer{"old": oldSigner, "new": newSigner} {
		if _, err := verifier.Verify(signTestToken(t, signer, AudienceRuntime, time.Minute), AudienceRuntime); err != nil {
			t.Fatalf("%s key token rejected: %v", name, err)
		}
	}
}

func TestES256KeyParsingRejectsUnsuitableKeys(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384DER, _ := x509.MarshalPKCS8PrivateKey(p384)
	p384PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p384DER})
	if _, err := NewES256Signer(p384PEM); err == nil {
		t.Fatal("P-384 signing key accepted for ES256")
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	if _, err := NewES256Signer(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER})); err == nil {
		t.Fatal("RSA signing key accepted for ES256")
	}
	if _, err := NewES256Signer([]byte("not a pem")); err == nil {
		t.Fatal("garbage accepted as a signing key")
	}

	privatePEM, _ := newTestKeyPair(t)
	if _, err := NewES256Verifier(privatePEM); err == nil {
		t.Fatal("a private key was accepted as a verification key")
	}
	if _, err := NewES256Verifier(); err == nil {
		t.Fatal("empty verification key set accepted")
	}
	p384Public, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	if _, err := NewES256Verifier(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: p384Public})); err == nil {
		t.Fatal("P-384 verification key accepted for ES256")
	}
}

func TestES256SignerAcceptsSEC1AndPKCS8EncodedKeys(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sec1, _ := x509.MarshalECPrivateKey(key)
	if _, err := NewES256Signer(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})); err != nil {
		t.Fatalf("SEC1 key rejected: %v", err)
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := NewES256Signer(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})); err != nil {
		t.Fatalf("PKCS8 key rejected: %v", err)
	}
}

func TestPlatformKeysFromEnvSignsAndVerifiesAndKeepsOldKeysForRotation(t *testing.T) {
	currentPrivate, _ := newTestKeyPair(t)
	oldPrivate, oldPublic := newTestKeyPair(t)
	t.Setenv(EnvSigningKey, string(currentPrivate))
	t.Setenv(EnvVerifyKeys, string(oldPublic))

	signer, verifier, err := PlatformKeysFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(signTestToken(t, signer, AudiencePlatform, time.Minute), AudiencePlatform); err != nil {
		t.Fatalf("current key token rejected: %v", err)
	}
	oldSigner, _ := NewES256Signer(oldPrivate)
	if _, err := verifier.Verify(signTestToken(t, oldSigner, AudiencePlatform, time.Minute), AudiencePlatform); err != nil {
		t.Fatalf("old key token rejected during rotation: %v", err)
	}

	t.Setenv(EnvSigningKey, "")
	if _, _, err := PlatformKeysFromEnv(); err == nil {
		t.Fatal("PlatformKeysFromEnv succeeded without a signing key")
	}
}

func TestVerifierFromEnvNeedsOnlyPublicKeys(t *testing.T) {
	privatePEM, publicPEM := newTestKeyPair(t)
	signer, _ := NewES256Signer(privatePEM)
	t.Setenv(EnvVerifyKeys, string(publicPEM))
	t.Setenv(EnvSigningKey, "")

	verifier, err := VerifierFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(signTestToken(t, signer, AudienceRuntime, time.Minute), AudienceRuntime); err != nil {
		t.Fatalf("token rejected: %v", err)
	}

	t.Setenv(EnvVerifyKeys, "")
	if _, err := VerifierFromEnv(); err == nil {
		t.Fatal("VerifierFromEnv succeeded without a verification key")
	}
}

func TestEnsureVerifyKeyPublishesTheSigningKeyAndKeepsOldKeys(t *testing.T) {
	currentPrivate, currentPublic := newTestKeyPair(t)
	_, oldPublic := newTestKeyPair(t)

	got, err := EnsureVerifyKey(currentPrivate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(currentPublic) {
		t.Fatalf("empty set = %q, want the signing key's public key", got)
	}

	unchanged, err := EnsureVerifyKey(currentPrivate, got)
	if err != nil || string(unchanged) != string(got) {
		t.Fatalf("set that already holds the key changed: %q err=%v", unchanged, err)
	}

	merged, err := EnsureVerifyKey(currentPrivate, oldPublic)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewES256Verifier(merged)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewES256Signer(currentPrivate)
	if _, err := verifier.Verify(signTestToken(t, signer, AudienceRuntime, time.Minute), AudienceRuntime); err != nil {
		t.Fatalf("signing key not trusted after merge: %v", err)
	}
	if len(verifier.keys) != 2 {
		t.Fatalf("merged set holds %d keys, want the old and the current key", len(verifier.keys))
	}
}

func TestEnsureVerifyKeyRefusesUnusableInput(t *testing.T) {
	privatePEM, _ := newTestKeyPair(t)
	if _, err := EnsureVerifyKey([]byte("not a key"), nil); err == nil {
		t.Fatal("invalid signing key accepted")
	}
	// A private key in the verification set would publish a secret.
	if _, err := EnsureVerifyKey(privatePEM, privatePEM); err == nil {
		t.Fatal("private key accepted as an existing verification set")
	}
}
