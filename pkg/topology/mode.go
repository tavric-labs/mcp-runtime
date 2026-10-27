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
	// ModePublic exposes a public catalog readable by anonymous clients and
	// writable by signed-in users, alongside the signed-in organization
	// catalog. Tenant namespaces remain reachable to their owners.
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

// PublicCatalogEnabled reports whether anonymous clients may browse the
// catalog (true only for ModePublic).
func (m Mode) PublicCatalogEnabled() bool {
	return m == ModePublic
}

// DefaultCatalogNamespace returns the primary catalog namespace for the
// active mode. Unknown modes return an empty namespace.
//
// Override precedence (first non-empty wins):
//  1. PLATFORM_CATALOG_NAMESPACE
//  2. MCP_PLATFORM_CATALOG_NAMESPACE
//  3. mode-specific override (PLATFORM_ORG_NAMESPACE / PLATFORM_PUBLIC_NAMESPACE)
//  4. built-in default (DefaultOrgCatalogNamespace / DefaultPublicCatalogNamespace)
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
	switch m {
	case ModeOrg:
		if v := strings.TrimSpace(os.Getenv("PLATFORM_ORG_NAMESPACE")); v != "" {
			return v
		}
		return DefaultOrgCatalogNamespace
	case ModePublic:
		if v := strings.TrimSpace(os.Getenv("PLATFORM_PUBLIC_NAMESPACE")); v != "" {
			return v
		}
		return DefaultPublicCatalogNamespace
	default:
		return ""
	}
}

// CatalogNamespaces returns the ordered list of catalog namespaces for the
// active mode. The first element is the namespace returned by
// DefaultCatalogNamespace; additional entries come from
// PLATFORM_CATALOG_NAMESPACES (with PLATFORM_PUBLIC_NAMESPACES as a
// public-mode-only fallback).
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
	if raw == "" && m == ModePublic {
		raw = strings.TrimSpace(os.Getenv("PLATFORM_PUBLIC_NAMESPACES"))
		if raw == "" {
			raw = strings.TrimSpace(os.Getenv("MCP_PLATFORM_PUBLIC_NAMESPACES"))
		}
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

// EnabledPublishScopes returns the publishing boundaries enabled by the mode.
// Public mode includes tenant, organization, and public publishing.
func (m Mode) EnabledPublishScopes() []Scope {
	switch m {
	case ModePublic:
		return []Scope{ScopeTenant, ScopeOrg, ScopePublic}
	case ModeOrg:
		return []Scope{ScopeTenant, ScopeOrg}
	default:
		return nil
	}
}

func orgCatalogNamespace() string {
	if v := strings.TrimSpace(os.Getenv("PLATFORM_ORG_NAMESPACE")); v != "" {
		return v
	}
	return DefaultOrgCatalogNamespace
}

// CatalogNamespacesForScope keeps organization and anonymous public catalogs
// separate even when both scopes are enabled on a public platform.
func (m Mode) CatalogNamespacesForScope(scope Scope) []string {
	switch {
	case scope == ScopeOrg && m == ModePublic:
		return []string{orgCatalogNamespace()}
	case scope == ScopeOrg && m == ModeOrg:
		return m.primaryCatalogNamespaces()
	case scope == ScopePublic && m == ModePublic:
		values := []string{}
		for _, ns := range m.primaryCatalogNamespaces() {
			// An organization namespace must never become anonymous through
			// a catalog override or an extra-namespace entry.
			if ns != orgCatalogNamespace() && ns != DefaultOrgCatalogNamespace {
				values = append(values, ns)
			}
		}
		return values
	default:
		return nil
	}
}

// CatalogNamespaces returns catalogs visible to signed-in users. Anonymous
// readers must use CatalogNamespacesForScope(ScopePublic).
func (m Mode) CatalogNamespaces() []string {
	if m == ModePublic {
		return dedupe(append(m.CatalogNamespacesForScope(ScopePublic), m.CatalogNamespacesForScope(ScopeOrg)...))
	}
	return m.primaryCatalogNamespaces()
}

// CatalogScope reports the publishing boundary of a catalog namespace.
func (m Mode) CatalogScope(namespace string) (Scope, bool) {
	for _, scope := range []Scope{ScopeOrg, ScopePublic} {
		for _, candidate := range m.CatalogNamespacesForScope(scope) {
			if strings.TrimSpace(namespace) == candidate {
				return scope, true
			}
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
