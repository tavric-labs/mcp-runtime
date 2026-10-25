package main

import (
	"encoding/json"
	"net/http"

	policypkg "mcp-runtime/pkg/policy"
)

// applyFilterResponse is the single response owner for the pre-proxy stages.
// Unknown/zero outcomes fail closed. The forwarding stage alone owns streaming.
func applyFilterResponse(ex *Exchange, response *FilterResponse) {
	ex.SkipAudit = false
	switch response.kind {
	case responseDenied:
		if response.Decision.Allowed || response.Decision.Status != 0 && (response.Decision.Status < 400 || response.Decision.Status > 599) {
			break
		}
		ex.Decision = writeDeniedResponse(ex.W, ex.Policy, ex.Inspection.ToolName(), response.Decision)
		return
	case responseForwarded:
		ex.Decision = response.Decision
		return
	case responseMetadata:
		if response.metadata == nil || response.metadata.status < 200 || response.metadata.status > 599 {
			break
		}
		ex.Decision, ex.SkipAudit = response.Decision, true
		writeMetadataResponse(ex.W, ex.R, response.metadata)
		return
	}
	ex.Decision = writeDeniedResponse(ex.W, ex.Policy, ex.Inspection.ToolName(),
		policypkg.Deny(http.StatusInternalServerError, "invalid_filter_response", ex.Decision.PolicyVersion))
}

type oauthResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
}

func writeMetadataResponse(w http.ResponseWriter, r *http.Request, metadata *oauthMetadataResponse) {
	if metadata.status != http.StatusMethodNotAllowed {
		w.Header().Set("content-type", "application/json")
	}
	w.WriteHeader(metadata.status)
	if r.Method == http.MethodHead || metadata.status == http.StatusMethodNotAllowed {
		return
	}
	if metadata.errorCode != "" {
		_ = json.NewEncoder(w).Encode(struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}{metadata.errorCode, metadata.message})
		return
	}
	_ = json.NewEncoder(w).Encode(oauthResourceMetadata{
		Resource: metadata.resource, AuthorizationServers: metadata.authorizationServers,
		BearerMethodsSupported: []string{"header"}, ScopesSupported: metadata.scopes,
	})
}
