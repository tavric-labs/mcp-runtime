package passport

import (
	"bytes"
	"testing"
	"time"
)

func TestEnsureKeyPairGeneratesWhenNoSigningKey(t *testing.T) {
	signing, verify, err := EnsureKeyPair(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewES256Signer(signing)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewES256Verifier(verify)
	if err != nil {
		t.Fatal(err)
	}
	token, err := Sign(signer, Principal{Subject: "u", Role: RoleUser}, time.Minute, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, AudienceRuntime); err != nil {
		t.Fatalf("generated verify set does not verify the generated key: %v", err)
	}
	if bytes.Contains(verify, []byte("PRIVATE")) {
		t.Fatal("verify set holds a private key")
	}
}

func TestEnsureKeyPairKeepsExistingKeysAndOldVerifyKeys(t *testing.T) {
	private, public, err := GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signing, verify, err := EnsureKeyPair(private, public)
	if err != nil || !bytes.Equal(signing, private) || !bytes.Equal(verify, public) {
		t.Fatalf("complete pair must be untouched: %v", err)
	}
	_, old, err := GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signing, verify, err = EnsureKeyPair(private, old)
	if err != nil || !bytes.Equal(signing, private) {
		t.Fatalf("signing key changed: %v", err)
	}
	if !bytes.Contains(verify, bytes.TrimSpace(old)) || !bytes.Contains(verify, bytes.TrimSpace(public)) {
		t.Fatal("verify set must keep the old key and add the signing key's public key")
	}
}

func TestEnsureKeyPairKeepsExistingVerifyKeysWhenGeneratingSigningKey(t *testing.T) {
	_, old, err := GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signing, verify, err := EnsureKeyPair(nil, old)
	if err != nil {
		t.Fatal(err)
	}
	if len(signing) == 0 || !bytes.Contains(verify, bytes.TrimSpace(old)) {
		t.Fatal("existing verify keys must survive a generated signing key")
	}
}

func TestEnsureKeyPairRejectsUnparsableKeys(t *testing.T) {
	if _, _, err := EnsureKeyPair([]byte("not a key"), nil); err == nil {
		t.Fatal("unparsable signing key must fail, not be replaced")
	}
	private, _, _ := GenerateES256KeyPair()
	if _, _, err := EnsureKeyPair(private, []byte("garbage")); err == nil {
		t.Fatal("unparsable verify set must fail")
	}
}
