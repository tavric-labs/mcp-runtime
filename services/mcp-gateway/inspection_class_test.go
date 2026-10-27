package main

import (
	"testing"
	"time"
)

func TestInspectionClassIsDerivedFromInspectedFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		inspection rpcInspection
		class      inspectionClass
		authorize  bool
		attempt    bool
	}{
		{"zero value is not RPC", rpcInspection{}, classNotRPC, false, false},
		{"tools/list", inspectedRPC("tools/list", "", RPCRequest), classOtherRPC, false, true},
		{"custom extension method", inspectedRPC("example.test/custom", "x", RPCRequest), classOtherRPC, false, true},
		{"tool call", inspectedRPC("tools/call", "echo", RPCRequest), classToolCall, true, true},
		{"tool call without a name", inspectedRPC("tools/call", "", RPCRequest), classToolCall, true, true},
		{"content type probe", failedRPCInspection(inspectionContentType), classInvalid, true, false},
		{"missing body", failedRPCInspection(inspectionMissingBody), classInvalid, true, true},
		{"oversized body", failedRPCInspection(inspectionBodyTooLarge), classInvalid, true, true},
		{"read failure", failedRPCInspection(inspectionReadFailed), classInvalid, true, true},
		{"malformed", failedRPCInspection(inspectionMalformed), classInvalid, true, true},
		{"missing method", failedRPCInspection(inspectionMissingMethod), classInvalid, true, true},
	} {
		got := tc.inspection
		if got.Class() != tc.class || got.RequiresAuthorization() != tc.authorize || got.IsRPCAttempt() != tc.attempt {
			t.Errorf("%s: class=%d authorize=%v attempt=%v", tc.name, got.Class(), got.RequiresAuthorization(), got.IsRPCAttempt())
		}
		if got.IsToolCall() != (tc.class == classToolCall) || got.Indeterminate() != (tc.class == classInvalid) {
			t.Errorf("%s: predicates disagree with class", tc.name)
		}
	}
}

// An invalid inspection can never classify as a tool call, even if a method
// string is present, so authorization always takes the fail-closed branch.
func TestInvalidInspectionWithMethodStaysInvalid(t *testing.T) {
	t.Parallel()
	got := rpcInspection{method: "tools/call", problem: inspectionMalformed}
	if got.IsToolCall() || !got.Indeterminate() {
		t.Fatalf("class=%d", got.Class())
	}
}

// Empty tool inputs on a tool call are evaluated against policy (policy
// decides), and an unparseable request is denied before any evaluation.
func TestAuthorizationOfEmptyAndMalformedToolInputs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body, reason string
	}{
		{"missing tool name", `{"method":"tools/call","params":{}}`, "tool_not_granted"},
		{"non-string tool name", `{"method":"tools/call","params":{"name":7}}`, "tool_not_granted"},
		{"no params", `{"method":"tools/call"}`, "tool_not_granted"},
		{"malformed body", `{"method":`, "rpc_inspection_failed"},
		{"blank method", `{"method":" "}`, "rpc_inspection_failed"},
	} {
		in := authorizationExample()
		in.Inspection = inspectRPCBody([]byte(tc.body))
		got := authorizeRequest(in, now)
		if got.Allowed || got.Reason != tc.reason || got.Status != 403 {
			t.Errorf("%s: allowed %+v", tc.name, got)
		}
	}
}
