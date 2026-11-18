package topology

import (
	"fmt"
	"strings"
)

type Scope string

const (
	ScopeTenant Scope = "tenant"
	ScopeOrg    Scope = "org"

	DefaultOrgCatalogNamespace = "mcp-servers-org"

	OrgRegistryAlias = "org"
)

func NormalizeScope(raw string) (Scope, error) {
	value := Scope(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "":
		return "", nil
	case ScopeTenant, ScopeOrg:
		return value, nil
	default:
		return "", fmt.Errorf("scope %q is invalid (use tenant or org)", raw)
	}
}

func ScopeCatalogNamespace(scope Scope) (string, bool) {
	if scope == ScopeOrg {
		return DefaultOrgCatalogNamespace, true
	}
	return "", false
}

func ScopeRegistryAlias(scope Scope) (string, bool) {
	if scope == ScopeOrg {
		return OrgRegistryAlias, true
	}
	return "", false
}
