package registry

import (
	"net/http"
	"testing"
)

func TestParseRegistryCredentialItemPathV1Delete(t *testing.T) {
	id, allowed, valid := parseRegistryCredentialItemPath(
		http.MethodDelete,
		"/api/v1/user/registry-credentials/rk_d95f9bd5-8bc0-4f78-9ed6-e4176fd38ac5",
	)
	if !allowed || !valid {
		t.Fatalf("allowed=%v valid=%v, want true true", allowed, valid)
	}
	if id != "rk_d95f9bd5-8bc0-4f78-9ed6-e4176fd38ac5" {
		t.Fatalf("id = %q, want credential id", id)
	}
}

// DELETE on the v1 path is the only way to revoke a credential: the pre-v1 path
// and the POST action form are gone, not kept for compatibility.
func TestParseRegistryCredentialItemPathRejectsOtherForms(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		allowed, valid     bool
	}{
		{"pre-v1 path", http.MethodDelete, "/api/user/registry-credentials/rk_test", true, false},
		{"POST revoke action", http.MethodPost, "/api/v1/user/registry-credentials/rk_test/revoke", false, false},
		{"nested path", http.MethodDelete, "/api/v1/user/registry-credentials/rk_test/extra", true, false},
		{"missing id", http.MethodDelete, "/api/v1/user/registry-credentials/", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, allowed, valid := parseRegistryCredentialItemPath(tc.method, tc.path)
			if allowed != tc.allowed || valid != tc.valid || id != "" {
				t.Fatalf("id=%q allowed=%v valid=%v, want id empty allowed=%v valid=%v", id, allowed, valid, tc.allowed, tc.valid)
			}
		})
	}
}
