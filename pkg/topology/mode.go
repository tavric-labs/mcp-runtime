package topology

import (
	"os"
	"strings"
)

// Mode is the canonical lower-case representation of a platform mode.
type Mode string

const (
	// ModeOrg exposes a shared organization catalog that signed-in users may
	// publish into. Tenant namespaces remain reachable to their owners.
	ModeOrg Mode = "org"
	// ModePublic is the hosted, internet-facing deployment profile. Its
	// catalogs and publish scopes are identical to ModeOrg (tenant and org);
	// it only adds the stricter setup login-configuration requirements. There
	// is no anonymous catalog.
	ModePublic Mode = "public"
)

// NormalizeMode returns the canonical mode for raw. The second return value is
// true when raw was empty or matched a known mode; unknown values fall back
// to ModeOrg and return false. Tenant is a publish scope, not a platform mode.
func NormalizeMode(raw string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModeOrg):
		return ModeOrg, true
	case string(ModePublic):
		return ModePublic, true
	default:
		return ModeOrg, false
	}
}

// ModeFromEnv reads the current platform mode from PLATFORM_MODE, falling back
// to MCP_PLATFORM_MODE. Unknown values resolve to ModeOrg.
func ModeFromEnv() Mode {
	raw := strings.TrimSpace(os.Getenv("PLATFORM_MODE"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("MCP_PLATFORM_MODE"))
	}
	mode, _ := NormalizeMode(raw)
	return mode
}

// SharedCatalogWritable reports whether signed-in users may publish into the
// active catalog namespace (true for ModeOrg and ModePublic).
func (m Mode) SharedCatalogWritable() bool {
	return m == ModeOrg || m == ModePublic
}

// DefaultCatalogNamespace returns the primary catalog namespace for the
// active mode. Unknown modes return an empty namespace.
//
// Override precedence (first non-empty wins):
//  1. PLATFORM_CATALOG_NAMESPACE
//  2. MCP_PLATFORM_CATALOG_NAMESPACE
//  3. PLATFORM_ORG_NAMESPACE
//  4. built-in default (DefaultOrgCatalogNamespace)
func (m Mode) DefaultCatalogNamespace() string {
	if m != ModeOrg && m != ModePublic {
		return ""
	}
	if v := strings.TrimSpace(os.Getenv("PLATFORM_CATALOG_NAMESPACE")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("MCP_PLATFORM_CATALOG_NAMESPACE")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("PLATFORM_ORG_NAMESPACE")); v != "" {
		return v
	}
	return DefaultOrgCatalogNamespace
}

// CatalogNamespaces returns the ordered list of catalog namespaces for the
// active mode. The first element is the namespace returned by
// DefaultCatalogNamespace; additional entries come from
// PLATFORM_CATALOG_NAMESPACES.
//
// Extra-namespace precedence is the same in every caller — the CLI, API, and
// UI all share this logic to avoid drift.
func (m Mode) primaryCatalogNamespaces() []string {
	if m != ModeOrg && m != ModePublic {
		return nil
	}
	raw := strings.TrimSpace(os.Getenv("PLATFORM_CATALOG_NAMESPACES"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("MCP_PLATFORM_CATALOG_NAMESPACES"))
	}
	values := make([]string, 0, 4)
	if def := m.DefaultCatalogNamespace(); def != "" {
		values = append(values, def)
	}
	for _, ns := range strings.Split(raw, ",") {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			values = append(values, ns)
		}
	}
	return dedupe(values)
}

// EnabledPublishScopes returns the publishing boundaries enabled by the mode:
// tenant and org in every known mode.
func (m Mode) EnabledPublishScopes() []Scope {
	if m != ModeOrg && m != ModePublic {
		return nil
	}
	return []Scope{ScopeTenant, ScopeOrg}
}

// CatalogNamespacesForScope returns the catalog namespaces of a publish scope.
// Only the org scope has shared catalogs; tenant namespaces are per team.
func (m Mode) CatalogNamespacesForScope(scope Scope) []string {
	if scope == ScopeOrg {
		return m.primaryCatalogNamespaces()
	}
	return nil
}

// CatalogNamespaces returns the shared catalog namespaces of the mode.
func (m Mode) CatalogNamespaces() []string {
	return m.primaryCatalogNamespaces()
}

// CatalogScope reports the publishing boundary of a catalog namespace.
func (m Mode) CatalogScope(namespace string) (Scope, bool) {
	for _, candidate := range m.CatalogNamespacesForScope(ScopeOrg) {
		if strings.TrimSpace(namespace) == candidate {
			return ScopeOrg, true
		}
	}
	return "", false
}

// IsCatalogNamespace reports whether namespace is one of the active mode's
// catalog namespaces.
func (m Mode) IsCatalogNamespace(namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return false
	}
	for _, ns := range m.CatalogNamespaces() {
		if ns == namespace {
			return true
		}
	}
	return false
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
