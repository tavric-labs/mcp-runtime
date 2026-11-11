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

func TestCatalogNamespaceEntriesListOnlyModeCatalogsInOrgMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")

	got := appendCatalogNamespaceEntries(nil)
	if len(got) != 1 {
		t.Fatalf("catalog namespaces = %#v, want only the org catalog entry", got)
	}
	if got[0]["namespace"] != defaultOrgCatalogNamespace {
		t.Fatalf("namespace = %#v, want %q", got[0]["namespace"], defaultOrgCatalogNamespace)
	}
	if got[0]["scope"] != "org" {
		t.Fatalf("scope = %#v, want org", got[0]["scope"])
	}
	if _, present := got[0]["is_shared"]; present {
		t.Fatalf("catalog entries must not carry the retired is_shared flag: %#v", got[0])
	}
}

func TestOrgCatalogEntryStaysOrgInPublicMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")

	got := catalogNamespaceEntry(defaultOrgCatalogNamespace)
	if got["scope"] != "org" {
		t.Fatalf("scope = %#v, want org", got["scope"])
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
