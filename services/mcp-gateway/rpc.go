package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	policypkg "mcp-runtime/pkg/policy"
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolParams struct {
	Name string `json:"name"`
}

func inspectRPCRequest(r *http.Request) rpcInspection {
	if r.Method != http.MethodPost {
		return rpcInspection{}
	}
	contentType := strings.ToLower(r.Header.Get("content-type"))
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		// Clearly not a JSON-RPC client (e.g. text/plain probe). Not an audit-worthy
		// MCP attempt.
		return failedRPCInspection(inspectionContentType, false)
	}
	// From here on the content-type is JSON (or absent), so treat failures as
	// genuine MCP client attempts that should produce audit events.
	if r.Body == nil || r.ContentLength == 0 {
		return failedRPCInspection(inspectionMissingBody, true)
	}
	if r.ContentLength > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge, true)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBodyBytes+1))
	if err != nil {
		return failedRPCInspection(inspectionReadFailed, true)
	}
	if len(body) == 0 {
		return failedRPCInspection(inspectionMissingBody, true)
	}
	if len(body) > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge, true)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return inspectRPCBody(body)
}

// inspectRPCBody extracts authorization inputs without HTTP or I/O. It retains
// the gateway's size limit even when called independently of body capture.
func inspectRPCBody(body []byte) rpcInspection {
	if len(body) == 0 {
		return failedRPCInspection(inspectionMissingBody, true)
	}
	if len(body) > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge, true)
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return failedRPCInspection(inspectionMalformed, true)
	}
	if strings.TrimSpace(req.Method) == "" {
		return failedRPCInspection(inspectionMissingMethod, true)
	}

	var toolName string
	if policypkg.IsToolCallMethod(req.Method) && len(req.Params) > 0 {
		var params toolParams
		if err := json.Unmarshal(req.Params, &params); err == nil {
			toolName = params.Name
		}
	}

	kind := RPCNotification
	if len(req.ID) > 0 {
		kind = RPCRequest
	}
	inspection := inspectedRPC(req.Method, toolName, kind)
	inspection.paramsRaw, inspection.idRaw = req.Params, req.ID
	return inspection
}
