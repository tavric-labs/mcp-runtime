package topology

import (
	"strings"
	"testing"
)

func TestNormalizeScope(t *testing.T) {
	got, err := NormalizeScope(" Org ")
	if err != nil {
		t.Fatalf("NormalizeScope() error = %v", err)
	}
	if got != ScopeOrg {
		t.Fatalf("NormalizeScope() = %q, want %q", got, ScopeOrg)
	}
	if got, err := NormalizeScope("tenant"); err != nil || got != ScopeTenant {
		t.Fatalf("NormalizeScope(tenant) = %q, %v", got, err)
	}
	if got, err := NormalizeScope(""); err != nil || got != "" {
		t.Fatalf("NormalizeScope(empty) = %q, %v", got, err)
	}
}

func TestNormalizeScopeRejectsPublicAndUnknown(t *testing.T) {
	for _, raw := range []string{"public", " Public ", "internet"} {
		_, err := NormalizeScope(raw)
		if err == nil {
			t.Fatalf("NormalizeScope(%q) expected error", raw)
		}
		if !strings.Contains(err.Error(), "use tenant or org") {
			t.Fatalf("NormalizeScope(%q) error %q must list tenant and org", raw, err)
		}
	}
}

func TestScopeRegistryAlias(t *testing.T) {
	if got, ok := ScopeRegistryAlias(ScopeOrg); !ok || got != OrgRegistryAlias {
		t.Fatalf("ScopeRegistryAlias(org) = %q %v", got, ok)
	}
	if _, ok := ScopeRegistryAlias(Scope("public")); ok {
		t.Fatal("public must not have a registry alias")
	}
	if _, ok := ScopeCatalogNamespace(Scope("public")); ok {
		t.Fatal("public must not have a catalog namespace")
	}
	if _, ok := ScopeRegistryAlias(ScopeTenant); ok {
		t.Fatal("tenant should not have a shared registry alias")
	}
}
