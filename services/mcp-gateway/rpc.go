package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolParams struct {
	Name string `json:"name"`
}

type targetParams struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// primitiveTarget returns the resource URI or prompt/skill name a request
// addresses. It is empty for every other method, so a stray field on an
// unrelated request never becomes an audited target.
func primitiveTarget(method string, params json.RawMessage) string {
	var byURI bool
	switch method {
	case "resources/read", "resources/subscribe", "resources/unsubscribe", "resources/directory/read":
		byURI = true
	case "prompts/get":
	case "skills/get":
		// The skills extension addresses a skill by its URI.
		byURI = true
	default:
		return ""
	}
	var p targetParams
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		// A wrongly typed sibling field must not hide a valid target.
		var raw map[string]json.RawMessage
		if json.Unmarshal(params, &raw) != nil {
			return ""
		}
		key := "name"
		if byURI {
			key = "uri"
		}
		var v string
		if json.Unmarshal(raw[key], &v) != nil {
			return ""
		}
		return v
	}
	if byURI {
		return p.URI
	}
	return p.Name
}

func inspectRPCRequest(r *http.Request) rpcInspection {
	if r.Method != http.MethodPost {
		return rpcInspection{}
	}
	contentType := strings.ToLower(r.Header.Get("content-type"))
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		// Clearly not a JSON-RPC client (e.g. text/plain probe). Not an audit-worthy
		// MCP attempt.
		return failedRPCInspection(inspectionContentType)
	}
	// From here on the content-type is JSON (or absent), so treat failures as
	// genuine MCP client attempts that should produce audit events.
	if r.Body == nil || r.ContentLength == 0 {
		return failedRPCInspection(inspectionMissingBody)
	}
	if r.ContentLength > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBodyBytes+1))
	if err != nil {
		return failedRPCInspection(inspectionReadFailed)
	}
	if len(body) == 0 {
		return failedRPCInspection(inspectionMissingBody)
	}
	if len(body) > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	inspection := inspectRPCBody(body)
	if inspection.problem == inspectionOK && headersDisagree(r.Header, inspection) {
		return failedRPCInspection(inspectionHeaderMismatch)
	}
	return inspection
}

// headersDisagree reports an Mcp-Method or Mcp-Name header that contradicts the
// body. Legacy clients send neither, so an absent header is never a mismatch.
func headersDisagree(h http.Header, in rpcInspection) bool {
	if v := strings.TrimSpace(h.Get("Mcp-Method")); v != "" && v != in.Method() {
		return true
	}
	v := strings.TrimSpace(h.Get("Mcp-Name"))
	if v == "" {
		return false
	}
	var want string
	switch in.Method() {
	case "tools/call":
		want = in.ToolName()
	case "resources/read", "prompts/get":
		want = in.Target()
	default:
		return false
	}
	return decodeHeaderValue(v) != want
}

// decodeHeaderValue undoes the spec's =?base64?...?= encoding for values that
// are not safe in a header. An undecodable sentinel is compared verbatim.
func decodeHeaderValue(v string) string {
	const prefix, suffix = "=?base64?", "?="
	if strings.HasPrefix(v, prefix) && strings.HasSuffix(v, suffix) && len(v) >= len(prefix)+len(suffix) {
		if raw, err := base64.StdEncoding.DecodeString(v[len(prefix) : len(v)-len(suffix)]); err == nil {
			return string(raw)
		}
	}
	return v
}

// completionTarget maps a completion reference to the read it reveals.
func completionTarget(params json.RawMessage) (method, target string) {
	var p struct {
		Ref struct {
			Type string `json:"type"`
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"ref"`
	}
	if json.Unmarshal(params, &p) != nil {
		return "resources/read", ""
	}
	switch p.Ref.Type {
	case "ref/prompt":
		return "prompts/get", p.Ref.Name
	case "ref/resource":
		return "resources/read", p.Ref.URI
	default:
		return "resources/read", ""
	}
}

// inspectRPCBody extracts authorization inputs without HTTP or I/O. It retains
// the gateway's size limit even when called independently of body capture.
func inspectRPCBody(body []byte) rpcInspection {
	if len(body) == 0 {
		return failedRPCInspection(inspectionMissingBody)
	}
	if len(body) > maxRPCBodyBytes {
		return failedRPCInspection(inspectionBodyTooLarge)
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return failedRPCInspection(inspectionMalformed)
	}
	if strings.TrimSpace(req.Method) == "" {
		return failedRPCInspection(inspectionMissingMethod)
	}

	var toolName string
	if len(req.Params) > 0 {
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
	inspection.target = primitiveTarget(req.Method, req.Params)
	inspection.listenTargets = listenTargets(req.Method, req.Params)
	if req.Method == "completion/complete" {
		inspection.completionAs, inspection.target = completionTarget(req.Params)
	}
	return inspection
}

// listenTargets returns the resource URIs a subscriptions/listen request asks
// to be notified about. Each must pass the same resource rules as a read.
func listenTargets(method string, params json.RawMessage) []string {
	if method != "subscriptions/listen" || len(params) == 0 {
		return nil
	}
	var p struct {
		Notifications struct {
			ResourceSubscriptions []string `json:"resourceSubscriptions"`
		} `json:"notifications"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	return p.Notifications.ResourceSubscriptions
}
