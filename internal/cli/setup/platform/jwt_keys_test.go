package platform

import (
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/passport"
)

func platformTokenFromSecrets(t *testing.T, data map[string]string) (signer passport.Signer, verifier passport.Verifier) {
	t.Helper()
	s, err := passport.NewES256Signer([]byte(data["JWT_SIGNING_KEY"]))
	if err != nil {
		t.Fatalf("JWT_SIGNING_KEY is not a usable ES256 key: %v", err)
	}
	v, err := passport.NewES256Verifier([]byte(data["JWT_VERIFY_KEY"]))
	if err != nil {
		t.Fatalf("JWT_VERIFY_KEY is not a usable ES256 key set: %v", err)
	}
	return s, v
}

func TestRenderGeneratesAnES256KeyPairWhenNoneExists(t *testing.T) {
	placeholderEnv(t, "")
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(nil))
	if err != nil {
		t.Fatal(err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if _, ok := data["JWT_SECRET"]; ok {
		t.Fatal("the shared JWT_SECRET must no longer be generated")
	}
	signer, verifier := platformTokenFromSecrets(t, data)
	token, err := passport.Sign(signer, passport.Principal{Subject: "user", Role: passport.RoleUser}, time.Minute, []string{passport.AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, passport.AudienceRuntime); err != nil {
		t.Fatalf("generated verification key does not verify tokens from the generated signing key: %v", err)
	}
	if strings.Contains(data["JWT_VERIFY_KEY"], "PRIVATE") {
		t.Fatal("the verification key set holds a private key")
	}
}

func TestRenderKeepsAnExistingSigningKeyAndVerifySet(t *testing.T) {
	placeholderEnv(t, "")
	privatePEM, publicPEM, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{
		"JWT_SIGNING_KEY": string(privatePEM),
		"JWT_VERIFY_KEY":  string(publicPEM),
	}))
	if err != nil {
		t.Fatal(err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["JWT_SIGNING_KEY"] != string(privatePEM) || data["JWT_VERIFY_KEY"] != string(publicPEM) {
		t.Fatal("an existing key pair was rotated by a re-run of setup")
	}
}

func TestRenderPublishesTheSigningKeyAndKeepsOldVerifyKeys(t *testing.T) {
	placeholderEnv(t, "")
	privatePEM, _, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	oldPrivate, oldPublic, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{
		"JWT_SIGNING_KEY": string(privatePEM),
		"JWT_VERIFY_KEY":  string(oldPublic),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, verifier := platformTokenFromSecrets(t, secretStringDataFromManifest(t, manifest))
	current, _ := passport.NewES256Signer(privatePEM)
	old, _ := passport.NewES256Signer(oldPrivate)
	for name, signer := range map[string]passport.Signer{"current": current, "old": old} {
		token, err := passport.Sign(signer, passport.Principal{Subject: "user", Role: passport.RoleUser}, time.Minute, []string{passport.AudienceRuntime})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(token, passport.AudienceRuntime); err != nil {
			t.Fatalf("%s key is not trusted after setup: %v", name, err)
		}
	}
}

func TestRenderRefusesToOverwriteAMalformedSigningKey(t *testing.T) {
	placeholderEnv(t, "")
	_, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{"JWT_SIGNING_KEY": "not a pem key"}))
	if err == nil {
		t.Fatal("a malformed existing signing key was silently replaced")
	}
	if strings.Contains(err.Error(), "not a pem key") {
		t.Fatalf("error must not echo the key material: %v", err)
	}
}

func TestRenderTreatsAPlaceholderSigningKeyLikeOtherPlaceholders(t *testing.T) {
	placeholderEnv(t, "")
	if _, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{"JWT_SIGNING_KEY": placeholderValue})); err == nil {
		t.Fatal("placeholder signing key preserved outside test mode")
	}
	placeholderEnv(t, "1")
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{"JWT_SIGNING_KEY": placeholderValue}))
	if err != nil {
		t.Fatalf("test mode must keep working: %v", err)
	}
	platformTokenFromSecrets(t, secretStringDataFromManifest(t, manifest))
}

func TestCredentialBundleDeliversThePrivateKeyOnlyToPlatformAPI(t *testing.T) {
	placeholderEnv(t, "")
	manifest, err := renderCredentialBundleWithSnapshots(readerWith(nil), map[string]map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	values := bundleValues(t, manifest)
	if values["mcp-platform-api-credentials"]["JWT_SIGNING_KEY"] == "" {
		t.Fatal("platform-api did not receive the signing key")
	}
	for _, name := range []string{"mcp-runtime-api-credentials", "mcp-analytics-api-credentials"} {
		if _, ok := values[name]["JWT_SIGNING_KEY"]; ok {
			t.Fatalf("%s received the signing key, so it could mint platform tokens", name)
		}
		if values[name]["JWT_VERIFY_KEY"] == "" {
			t.Fatalf("%s did not receive the verification key", name)
		}
		if _, ok := values[name]["JWT_SECRET"]; ok {
			t.Fatalf("%s still receives the shared JWT_SECRET", name)
		}
	}
	if values["mcp-runtime-api-credentials"]["JWT_VERIFY_KEY"] != values["mcp-platform-api-credentials"]["JWT_VERIFY_KEY"] {
		t.Fatal("services were given different verification keys")
	}
}
