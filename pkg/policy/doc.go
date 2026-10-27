// Package policy is the gateway authorization model.
//
// A rendered Document holds tools, grants and session bindings. Compile indexes
// it, and Authorize evaluates one MCP request into a Decision with a status,
// reason, required and effective trust, risk level, and the matched grant and
// session. It supports monitor mode (would-deny), delegated-header policies and
// a revision stamp. Schema versions v1 to v6 each gate one feature.
//
// It is pure data and rules, shared by the operator that renders policy, the
// gateway that enforces it and the CLI that inspects it.
package policy
