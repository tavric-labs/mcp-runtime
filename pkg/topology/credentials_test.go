package topology

import "testing"

// A service that only verifies tokens must never receive the key that signs
// them, or it could mint platform tokens.
func TestOnlyPlatformAPIReceivesTheJWTSigningKey(t *testing.T) {
	for _, set := range CredentialSets() {
		for _, key := range set.Keys {
			if key == "JWT_SECRET" {
				t.Fatalf("%s still carries the removed shared JWT_SECRET", set.Name)
			}
			if key == "JWT_SIGNING_KEY" && set.Name != "mcp-platform-api-credentials" {
				t.Fatalf("%s receives JWT_SIGNING_KEY; only platform-api may", set.Name)
			}
		}
	}
	owner, ok := CredentialOwner("JWT_SIGNING_KEY")
	if !ok || owner != "mcp-platform-api-credentials" {
		t.Fatalf("JWT_SIGNING_KEY owner = %q, %t; want platform-api's credentials", owner, ok)
	}
	for _, name := range []string{"mcp-runtime-api-credentials", "mcp-analytics-api-credentials"} {
		found := false
		for _, set := range CredentialSets() {
			if set.Name != name {
				continue
			}
			for _, key := range set.Keys {
				if key == "JWT_VERIFY_KEY" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("%s does not receive JWT_VERIFY_KEY", name)
		}
	}
}
