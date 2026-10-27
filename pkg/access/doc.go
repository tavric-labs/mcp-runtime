// Package access is the Kubernetes-free model of MCP access governance.
//
// It defines grants (MCPAccessGrantSpec and status), agent sessions, the
// request types for applying them, summaries, resource-name validation and
// server references, plus the allow and deny decisions they produce. The
// specvalidation subpackage holds the pure spec checks shared with the CRD
// types. Persistence is not here: the runtime API stores these objects through
// its own Kubernetes adapter, and platform-api owns sessions in Postgres.
//
// It depends only on policy, sessionstate and topology, so any service can use
// it without pulling in a Kubernetes client.
package access
