// Package controlplane lists, applies and reports on MCPServer resources and
// their Deployments.
//
// Manager wraps k8sclient.Clients. ListServers combines each MCPServer with its
// rollout status into ServerInfo, and the apply, get and delete helpers give the
// CLI, operator and runtime API one consistent view of server state, including
// the public MCP endpoint of a server.
package controlplane
