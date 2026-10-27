// Package kubeworkload defines how managed MCP workloads are secured.
//
// It provides the restricted pod and container security contexts, the workload
// ServiceAccount, the operator's secret and trust-bundle access, the per-server
// ingress NetworkPolicy, and parsing of the cluster-admin egress CIDRs that
// become pod egress ipBlocks (ParseEgressCIDRs, EgressBlocks). The default route
// is rejected so a cluster cannot open every address by accident.
package kubeworkload
