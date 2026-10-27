package topology

import (
	"fmt"
	"strings"
)

type Scope string

const (
	ScopeTenant Scope = "tenant"
	ScopeOrg    Scope = "org"
	ScopePublic Scope = "public"

	DefaultOrgCatalogNamespace    = "mcp-servers-org"
	DefaultPublicCatalogNamespace = "mcp-servers-public"

	OrgRegistryAlias    = "org"
	PublicRegistryAlias = "public"
)

func NormalizeScope(raw string) (Scope, error) {
	value := Scope(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "":
		return "", nil
	case ScopeTenant, ScopeOrg, ScopePublic:
		return value, nil
	default:
		return "", fmt.Errorf("scope %q is invalid (use tenant, org, or public)", raw)
	}
}

func ScopeCatalogNamespace(scope Scope) (string, bool) {
	switch scope {
	case ScopeOrg:
		return DefaultOrgCatalogNamespace, true
	case ScopePublic:
		return DefaultPublicCatalogNamespace, true
	default:
		return "", false
	}
}

func ScopeRegistryAlias(scope Scope) (string, bool) {
	switch scope {
	case ScopeOrg:
		return OrgRegistryAlias, true
	case ScopePublic:
		return PublicRegistryAlias, true
	default:
		return "", false
	}
}
