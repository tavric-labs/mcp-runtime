package topology

const (
	// DefaultRegistryHost names the registry used when none is configured.
	DefaultRegistryHost = "registry.local"

	MCPServerPort       = 8088
	MCPGatewayPort      = 8091
	MCPServersNamespace = "mcp-servers"
	// AnalyticsTokenAudience must match the audience accepted by ingest.
	AnalyticsTokenAudience = "mcp-ingest"
)
