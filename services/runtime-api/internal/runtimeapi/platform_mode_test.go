package runtimeapi

import (
	"testing"
)

func TestDefaultCatalogNamespaceForModeUsesCatalogOverrideInDefaultOrgMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom-catalog")

	if got := defaultCatalogNamespaceForMode(); got != "custom-catalog" {
		t.Fatalf("default catalog namespace = %q, want custom-catalog", got)
	}
}

func TestDefaultCatalogNamespaceForModeUsesCatalogOverrideInSharedMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom-catalog")

	if got := defaultCatalogNamespaceForMode(); got != "custom-catalog" {
		t.Fatalf("default catalog namespace = %q, want custom-catalog", got)
	}
}

func TestPlatformModeUsesStrictValues(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "tenent")

	if got := PlatformMode(); got != platformModeOrg {
		t.Fatalf("platform mode = %q, want org fallback", got)
	}
}

func TestCatalogNamespaceEntriesIncludeSharedCatalogInTenantMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")

	got := appendCatalogNamespaceEntries(nil)
	if len(got) != 2 {
		t.Fatalf("catalog namespaces = %#v, want shared and org catalog entries", got)
	}
	if got[0]["namespace"] != sharedCatalogNamespace {
		t.Fatalf("namespace = %#v, want %q", got[0]["namespace"], sharedCatalogNamespace)
	}
	if got[0]["scope"] != "shared" {
		t.Fatalf("scope = %#v, want shared", got[0]["scope"])
	}
	if got[0]["is_shared"] != true {
		t.Fatalf("is_shared = %#v, want true", got[0]["is_shared"])
	}
}

func TestSharedCatalogEntryStaysSharedInPublicMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")

	got := catalogNamespaceEntry(sharedCatalogNamespace)
	if got["scope"] != "shared" {
		t.Fatalf("scope = %#v, want shared", got["scope"])
	}
	if _, ok := got["is_public"]; ok {
		t.Fatalf("is_public must no longer be reported: %#v", got)
	}
}

func TestPublicModePrincipalCanReadOrgCatalogAndOwnedNamespaces(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	p := principal{
		Role:      roleUser,
		Subject:   "user-1",
		Namespace: "user-1",
		AllowedNamespaces: []string{
			"user-1",
		},
	}
	if !principalCanReadNamespace(p, defaultOrgCatalogNamespace) {
		t.Fatalf("expected user to read org catalog namespace %q", defaultOrgCatalogNamespace)
	}
	if principalCanReadNamespace(p, "mcp-servers-public") {
		t.Fatal("mcp-servers-public is no longer a catalog namespace")
	}
	if !principalCanReadNamespace(p, "user-1") {
		t.Fatal("expected user to read owned namespace in public mode")
	}
	if principalCanReadNamespace(p, "other-user") {
		t.Fatal("did not expect user to read unrelated namespace")
	}
}
