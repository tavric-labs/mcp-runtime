package identity

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestValidateNameAcceptsArbitraryCustomerHeaders(t *testing.T) {
	for _, name := range []string{"X-Example-Credential", "Private-Token", "Authorization", "JOB-TOKEN"} {
		if err := ValidateCredentialHeaderName(name); err != nil {
			t.Fatalf("ValidateCredentialHeaderName(%q) = %v", name, err)
		}
	}
}

func TestValidateNameRejectsReservedHeaders(t *testing.T) {
	for _, name := range []string{"", "Host", "Connection", "X-Forwarded-For", "X-MCP-Verified-SPIFFE-ID", "Mcp-Session-Id", "Cookie", "Transfer-Encoding", "bad name"} {
		if err := ValidateCredentialHeaderName(name); err == nil {
			t.Fatalf("ValidateCredentialHeaderName(%q) succeeded", name)
		}
	}
}

func TestValidateValueHidesTheCredential(t *testing.T) {
	secret := "super-secret-token"
	err := ValidateCredentialValue("  ")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("empty error = %v", err)
	}
	err = ValidateCredentialValue(secret + "\n")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("control error = %v", err)
	}
	if err := ValidateCredentialValue(secret); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCredentialValue("Bearer " + secret); err != nil {
		t.Fatalf("bearer value rejected: %v", err)
	}
}

func TestCredentialPolicyChecksPresenceAndDuplicates(t *testing.T) {
	names := []string{"X-Example-Credential", "Private-Token"}
	for _, tc := range []struct {
		name, presence string
		headers        http.Header
		want           error
	}{
		{name: "any", presence: "any", headers: http.Header{"X-Example-Credential": {"one"}}},
		{name: "default any", headers: http.Header{"X-Example-Credential": {"one"}}},
		{name: "all incomplete", presence: "all", headers: http.Header{"X-Example-Credential": {"one"}}, want: ErrMissingCredential},
		{name: "all present", presence: "all", headers: http.Header{"X-Example-Credential": {"one"}, "Private-Token": {"two"}}},
		{name: "missing", want: ErrMissingCredential},
		{name: "empty", headers: http.Header{"X-Example-Credential": {"  "}}, want: ErrEmptyCredential},
		{name: "duplicate", headers: http.Header{"X-Example-Credential": {"one", "two"}}, want: ErrDuplicateCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewCredentialPolicy(names, tc.presence)
			if err != nil {
				t.Fatal(err)
			}
			if err := policy.Check(tc.headers); !errors.Is(err, tc.want) {
				t.Fatalf("check=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestCredentialPolicyRejectsInvalidConfigAndOwnsItsNames(t *testing.T) {
	if _, err := NewCredentialPolicy([]string{"X-Forwarded-For"}, "any"); !errors.Is(err, ErrInvalidCredentialConfig) {
		t.Fatalf("reserved header accepted: %v", err)
	}
	if err := (CredentialPolicy{}).Check(http.Header{"Authorization": {"private"}}); !errors.Is(err, ErrInvalidCredentialConfig) {
		t.Fatalf("zero policy accepted: %v", err)
	}
	names := []string{"Private-Token"}
	policy, err := NewCredentialPolicy(names, "any")
	if err != nil {
		t.Fatal(err)
	}
	names[0] = "Authorization"
	if err := policy.Check(http.Header{"Private-Token": {"secret"}}); err != nil {
		t.Fatalf("caller mutated the header policy: %v", err)
	}
}
