package main

import (
	"encoding/json"
	policypkg "mcp-runtime/pkg/policy"
)

// RPCMethod is deliberately open: core revisions and extensions can introduce
// methods without changing the envelope, audit contract, or filter interfaces.
type RPCMethod string

type RPCMessageKind string

const (
	RPCRequest      RPCMessageKind = "request"
	RPCNotification RPCMessageKind = "notification"
)

type inspectionProblem uint8

const (
	inspectionOK inspectionProblem = iota
	inspectionMissingBody
	inspectionBodyTooLarge
	inspectionReadFailed
	inspectionContentType
	inspectionMalformed
	inspectionMissingMethod
)

// rpcInspection has one owner: the inspector. Consumers cannot independently
// set a tool-call flag that disagrees with its method. No params, credentials,
// request IDs, or arbitrary extension payloads are retained in audit metadata.
type rpcInspection struct {
	method     RPCMethod
	toolName   policypkg.ToolName
	kind       RPCMessageKind
	problem    inspectionProblem
	rpcAttempt bool
	// Raw JSON is private dispatch data for idempotency hashing, never audit data.
	paramsRaw json.RawMessage
	idRaw     json.RawMessage
}

func inspectedRPC(method, tool string, kind RPCMessageKind) rpcInspection {
	return rpcInspection{method: RPCMethod(method), toolName: policypkg.ToolName(tool), kind: kind}
}

func failedRPCInspection(problem inspectionProblem, auditAttempt bool) rpcInspection {
	return rpcInspection{problem: problem, rpcAttempt: auditAttempt}
}

func (r rpcInspection) Method() string       { return string(r.method) }
func (r rpcInspection) ToolName() string     { return string(r.toolName) }
func (r rpcInspection) Kind() RPCMessageKind { return r.kind }
func (r rpcInspection) IsToolCall() bool {
	return !r.Indeterminate() && policypkg.IsToolCallMethod(r.Method())
}
func (r rpcInspection) Indeterminate() bool { return r.problem != inspectionOK }
func (r rpcInspection) IsRPCAttempt() bool  { return r.rpcAttempt || r.method != "" }
func (r rpcInspection) FailureReason() string {
	if r.Indeterminate() {
		return "rpc_inspection_failed"
	}
	return ""
}
