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

// inspectionClass is the one classification every consumer branches on. It is
// derived from the inspector's facts, so a method and its class cannot
// disagree. Go has no exhaustive switches: consumers keep a default branch
// that fails closed.
type inspectionClass uint8

const (
	// classNotRPC is the zero value: traffic the inspector never parsed (for
	// example a non-POST request). It is not an authorization subject.
	classNotRPC inspectionClass = iota
	// classOtherRPC is a well-formed RPC whose method is not a tool call.
	classOtherRPC
	// classToolCall is a well-formed tool invocation.
	classToolCall
	// classInvalid is an uninspectable request; it fails closed at authorization.
	classInvalid
)

// rpcInspection has one owner: the inspector. Consumers cannot independently
// set a tool-call flag that disagrees with its method, and whether a failed
// inspection is an audited RPC attempt follows from its problem. No params,
// credentials, request IDs, or arbitrary extension payloads are retained in
// audit metadata.
type rpcInspection struct {
	method   RPCMethod
	toolName policypkg.ToolName
	kind     RPCMessageKind
	problem  inspectionProblem
	// Raw JSON is private dispatch data for idempotency hashing, never audit data.
	paramsRaw json.RawMessage
	idRaw     json.RawMessage
}

func inspectedRPC(method, tool string, kind RPCMessageKind) rpcInspection {
	return rpcInspection{method: RPCMethod(method), toolName: policypkg.ToolName(tool), kind: kind}
}

func failedRPCInspection(problem inspectionProblem) rpcInspection {
	return rpcInspection{problem: problem}
}

func (r rpcInspection) Class() inspectionClass {
	switch {
	case r.problem != inspectionOK:
		return classInvalid
	case r.method == "":
		return classNotRPC
	case policypkg.IsToolCallMethod(string(r.method)):
		return classToolCall
	default:
		return classOtherRPC
	}
}

func (r rpcInspection) Method() string       { return string(r.method) }
func (r rpcInspection) ToolName() string     { return string(r.toolName) }
func (r rpcInspection) Kind() RPCMessageKind { return r.kind }
func (r rpcInspection) IsToolCall() bool     { return r.Class() == classToolCall }
func (r rpcInspection) Indeterminate() bool  { return r.Class() == classInvalid }

// RequiresAuthorization reports whether grant and session evaluation applies:
// tool calls, and invalid input, which is evaluated only to be denied. Other
// RPC and non-RPC traffic intentionally bypasses it.
func (r rpcInspection) RequiresAuthorization() bool {
	switch r.Class() {
	case classToolCall, classInvalid:
		return true
	default:
		return false
	}
}

// IsRPCAttempt reports whether the request is audited as an MCP attempt. A
// non-JSON content type is a probe rather than an MCP client, so it is not.
func (r rpcInspection) IsRPCAttempt() bool {
	if r.problem != inspectionOK {
		return r.problem != inspectionContentType
	}
	return r.method != ""
}

func (r rpcInspection) FailureReason() string {
	if r.Indeterminate() {
		return "rpc_inspection_failed"
	}
	return ""
}
