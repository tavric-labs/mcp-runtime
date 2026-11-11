package topology

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in    string
		want  Mode
		known bool
	}{
		{"", ModeOrg, true},
		{"tenant", ModeOrg, false},
		{"TENANT", ModeOrg, false},
		{"  tenant  ", ModeOrg, false},
		{"org", ModeOrg, true},
		{"Org", ModeOrg, true},
		{"public", ModePublic, true},
		{"PUBLIC", ModePublic, true},
		{"tenent", ModeOrg, false},
		{"random", ModeOrg, false},
	}
	for _, tc := range cases {
		got, ok := NormalizeMode(tc.in)
		if got != tc.want || ok != tc.known {
			t.Errorf("NormalizeMode(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.known)
		}
	}
}

func TestTwoModesAndTwoPublishScopes(t *testing.T) {
	if _, ok := NormalizeMode("tenant"); ok {
		t.Fatal("tenant must only be a publish scope")
	}
	if got, ok := NormalizeMode(""); !ok || got != ModeOrg {
		t.Fatalf("default mode=%s known=%t", got, ok)
	}
	if got := ModeOrg.EnabledPublishScopes(); !reflect.DeepEqual(got, []Scope{ScopeTenant, ScopeOrg}) {
		t.Fatalf("org scopes=%v", got)
	}
	if got := ModePublic.EnabledPublishScopes(); !reflect.DeepEqual(got, []Scope{ScopeTenant, ScopeOrg}) {
		t.Fatalf("public scopes=%v", got)
	}
}

func TestOrgScopeNamespacesFollowOverridesInEveryMode(t *testing.T) {
	t.Setenv("PLATFORM_ORG_NAMESPACE", "private-org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "extra")
	for _, mode := range []Mode{ModeOrg, ModePublic} {
		if got := mode.CatalogNamespacesForScope(ScopeOrg); !reflect.DeepEqual(got, []string{"private-org", "extra"}) {
			t.Fatalf("%s org namespaces=%v", mode, got)
		}
		if got := mode.CatalogNamespacesForScope(Scope("public")); got != nil {
			t.Fatalf("%s public scope namespaces=%v, want none", mode, got)
		}
		if scope, ok := mode.CatalogScope("private-org"); !ok || scope != ScopeOrg {
			t.Fatalf("%s private-org scope=%s ok=%t", mode, scope, ok)
		}
		if _, ok := mode.CatalogScope("mcp-servers-public"); ok {
			t.Fatalf("%s must not treat mcp-servers-public as a catalog", mode)
		}
	}
}

func TestFromEnvPrefersPlatformMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("MCP_PLATFORM_MODE", "public")
	if got := ModeFromEnv(); got != ModeOrg {
		t.Fatalf("ModeFromEnv() = %v, want %v", got, ModeOrg)
	}
}

func TestFromEnvFallsBackToMCPPlatformMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "")
	t.Setenv("MCP_PLATFORM_MODE", "public")
	if got := ModeFromEnv(); got != ModePublic {
		t.Fatalf("ModeFromEnv() = %v, want %v", got, ModePublic)
	}
}

func TestFromEnvUnknownFallsBackToTenant(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "weird")
	if got := ModeFromEnv(); got != ModeOrg {
		t.Fatalf("ModeFromEnv() = %v, want %v", got, ModeOrg)
	}
}

func TestSharedCatalogWritable(t *testing.T) {
	cases := map[Mode]bool{
		Mode("invalid"): false,
		ModeOrg:         true,
		ModePublic:      true,
	}
	for mode, want := range cases {
		if got := mode.SharedCatalogWritable(); got != want {
			t.Errorf("(%v).SharedCatalogWritable() = %v, want %v", mode, got, want)
		}
	}
}

func TestDefaultCatalogNamespaceTenant(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom")
	if got := Mode("invalid").DefaultCatalogNamespace(); got != "" {
		t.Fatalf("tenant DefaultCatalogNamespace = %q, want empty", got)
	}
}

func TestDefaultCatalogNamespacePrecedence(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "primary")
	t.Setenv("MCP_PLATFORM_CATALOG_NAMESPACE", "secondary")
	t.Setenv("PLATFORM_ORG_NAMESPACE", "org-override")
	if got := ModeOrg.DefaultCatalogNamespace(); got != "primary" {
		t.Fatalf("org DefaultCatalogNamespace = %q, want primary", got)
	}
}

func TestDefaultCatalogNamespaceOrgOverride(t *testing.T) {
	t.Setenv("PLATFORM_ORG_NAMESPACE", "org-only")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACE", "ignored")
	for _, mode := range []Mode{ModeOrg, ModePublic} {
		if got := mode.DefaultCatalogNamespace(); got != "org-only" {
			t.Fatalf("%s DefaultCatalogNamespace = %q, want org-only", mode, got)
		}
	}
}

func TestDefaultCatalogNamespaceBuiltinDefaults(t *testing.T) {
	for _, mode := range []Mode{ModeOrg, ModePublic} {
		if got := mode.DefaultCatalogNamespace(); got != DefaultOrgCatalogNamespace {
			t.Fatalf("%s DefaultCatalogNamespace = %q, want %q", mode, got, DefaultOrgCatalogNamespace)
		}
	}
}

func TestCatalogNamespacesTenantIsEmpty(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "ignored")
	if got := Mode("invalid").CatalogNamespaces(); got != nil {
		t.Fatalf("tenant CatalogNamespaces = %v, want nil", got)
	}
}

func TestCatalogNamespacesDedupes(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "mcp-servers-org, extra, mcp-servers-org , other")
	got := ModeOrg.CatalogNamespaces()
	want := []string{"mcp-servers-org", "extra", "other"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CatalogNamespaces = %v, want %v", got, want)
	}
}

func TestCatalogNamespacesIgnoreRemovedPublicEnv(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "")
	t.Setenv("MCP_PLATFORM_CATALOG_NAMESPACES", "")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACES", "mcp-servers-public, preview-extra")
	got := ModePublic.CatalogNamespaces()
	want := []string{DefaultOrgCatalogNamespace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public-mode CatalogNamespaces = %v, want %v", got, want)
	}
}

func TestIsCatalogNamespace(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "extra-namespace")
	cases := []struct {
		mode Mode
		ns   string
		want bool
	}{
		{Mode("invalid"), "mcp-servers-org", false},
		{ModeOrg, "mcp-servers-org", true},
		{ModeOrg, "extra-namespace", true},
		{ModeOrg, "unknown", false},
		{ModeOrg, "", false},
		{ModePublic, "mcp-servers-org", true},
		{ModePublic, "  mcp-servers-org  ", true},
		{ModePublic, "mcp-servers-public", false},
	}
	for _, tc := range cases {
		if got := tc.mode.IsCatalogNamespace(tc.ns); got != tc.want {
			t.Errorf("(%v).IsCatalogNamespace(%q) = %v, want %v", tc.mode, tc.ns, got, tc.want)
		}
	}
}
