// Package topology is the layout of the platform: what runs where, how it is
// addressed and which catalogs exist. It has no repository imports and does not
// provision resources or authorize requests.
//
//   - Components and placement: the component catalog, owners, namespaces, ports,
//     credential Secret placement and service hosts.
//   - Mode and scopes: the org and public platform modes, the tenant, org and
//     public publish scopes, and the catalog namespaces they imply. Anonymous
//     reads stay limited to public namespaces.
//   - Public routes: validation of customer-facing paths and the default MCP
//     route.
//   - Hosts and registry: resolution of the registry, MCP and platform hostnames
//     from the environment, and rewriting of image references for push, display
//     and in-cluster pulls.
package topology
