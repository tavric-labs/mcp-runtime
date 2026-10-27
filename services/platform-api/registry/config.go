package registry

import "mcp-runtime/pkg/platforminventory"

// NewAuthzConfigFromEnv builds registry authorization from the shared mode rules.
func NewAuthzConfigFromEnv() *AuthzConfig {
	mode := platforminventory.ModeFromEnv()
	catalogNamespaces := make(map[string]struct{})
	for _, namespace := range mode.CatalogNamespaces() {
		catalogNamespaces[namespace] = struct{}{}
	}
	return &AuthzConfig{mode: string(mode), catalogNamespaces: catalogNamespaces}
}
