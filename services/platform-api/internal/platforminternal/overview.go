package platforminternal

import (
	"context"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/apiwire"
	"net/http"
)

func (h Handler) overviewCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apikit.WriteMethodNotAllowed(w)
		return
	}
	store, ok := h.Store.(interface {
		OverviewCounts(context.Context) (apiwire.OverviewCounts, error)
	})
	if !ok {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "overview unavailable")
		return
	}
	out, err := store.OverviewCounts(r.Context())
	if err != nil {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeQueryFailed, "overview counts unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
