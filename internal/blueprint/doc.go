// Package blueprint is the .mcp server definition used by the authoring flow.
//
// RegistryFile and ServerMetadata describe a server in a small YAML file that
// mirrors the hand-authored parts of MCPServerSpec: image, route, resources,
// environment, tools, auth, policy, gateway, analytics and rollout. Load applies
// defaults and validation (publish scope, credential bindings, header auth) so
// every consumer sees a normalized definition.
//
// Rendering a blueprint into an MCPServer manifest lives in
// internal/cli/server/crdgen; hostnames and registry rewriting live in topology.
// Keep the schema aligned with api/v1alpha1.MCPServerSpec.
package blueprint
