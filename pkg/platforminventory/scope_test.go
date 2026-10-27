package platforminventory

import "testing"

func TestNormalizeScope(t *testing.T) {
	got, err := NormalizeScope(" Public ")
	if err != nil {
		t.Fatalf("NormalizeScope() error = %v", err)
	}
	if got != ScopePublic {
		t.Fatalf("NormalizeScope() = %q, want %q", got, ScopePublic)
	}
	if _, err := NormalizeScope("internet"); err == nil {
		t.Fatal("expected invalid scope error")
	}
}

func TestScopeRegistryAlias(t *testing.T) {
	if got, ok := ScopeRegistryAlias(ScopePublic); !ok || got != PublicRegistryAlias {
		t.Fatalf("ScopeRegistryAlias(public) = %q %v", got, ok)
	}
	if _, ok := ScopeRegistryAlias(ScopeTenant); ok {
		t.Fatal("tenant should not have a shared registry alias")
	}
}
