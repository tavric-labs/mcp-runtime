package platform

import "testing"

// A second setup pass reads back the Secret written by the first pass. The
// rendered signing and verification keys must be byte-identical, otherwise
// platform-api would roll again with a different key on every re-run.
func TestPlatformTokenKeyPairIsIdempotentAcrossSetupPasses(t *testing.T) {
	stored := map[string]string{}
	read := func(_, _, key string) (string, error) { return stored[key], nil }

	signing1, verify1, err := platformTokenKeyPair(read)
	if err != nil {
		t.Fatal(err)
	}
	if signing1 == "" || verify1 == "" {
		t.Fatal("first pass must generate both keys")
	}
	stored["JWT_SIGNING_KEY"], stored["JWT_VERIFY_KEY"] = signing1, verify1

	for pass := 2; pass <= 3; pass++ {
		signing, verify, err := platformTokenKeyPair(read)
		if err != nil {
			t.Fatal(err)
		}
		if signing != signing1 {
			t.Fatalf("pass %d: JWT_SIGNING_KEY changed", pass)
		}
		if verify != verify1 {
			t.Fatalf("pass %d: JWT_VERIFY_KEY changed", pass)
		}
	}
}
