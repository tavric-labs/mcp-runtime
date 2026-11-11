package topology

const (
	// DefaultRegistryHost names the registry used when none is configured.
	DefaultRegistryHost = "registry.local"

	MCPServerPort  = 8088
	MCPGatewayPort = 8091
	// DefaultServerNamespace is the namespace used when a command or resource
	// omits one. There is no separate base namespace: servers live in a
	// catalog namespace (organization by default) or a team namespace.
	DefaultServerNamespace = DefaultOrgCatalogNamespace
	// AnalyticsTokenAudience must match the audience accepted by ingest.
	AnalyticsTokenAudience = "mcp-ingest"
)
