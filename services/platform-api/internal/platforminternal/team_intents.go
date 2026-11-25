package platforminternal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
)

// TeamIntentStore is implemented by PlatformStore when durable team namespace
// intent persistence is available.
type TeamIntentStore interface {
	ListTeamIntents(ctx context.Context) ([]apikit.TeamNamespaceIntent, error)
	GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error)
	ClaimTeamIntentFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error)
	RecordTeamIntentReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error)
	ClaimTeamIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error)
	RecordTeamIntentObservation(ctx context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error)
}

func (h Handler) teamIntentStore() (TeamIntentStore, bool) {
	store, ok := h.Store.(TeamIntentStore)
	return store, ok
}

func (h Handler) teamIntents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apikit.WriteMethodNotAllowed(w)
		return
	}
	store, ok := h.teamIntentStore()
	if !ok {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, teamintent.ErrDisabled.Error())
		return
	}
	items, err := store.ListTeamIntents(r.Context())
	if err != nil {
		writeTeamIntentError(w, err, "failed to list team intents")
		return
	}
	writeJSON(w, http.StatusOK, apikit.TeamNamespaceIntentListResponse{Intents: items})
}

func (h Handler) teamIntentPath(w http.ResponseWriter, r *http.Request) {
	store, ok := h.teamIntentStore()
	if !ok {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, teamintent.ErrDisabled.Error())
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/team-intents/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "team intent not found")
		return
	}
	if len(parts) == 1 && parts[0] == "observer-lease" {
		if r.Method != http.MethodPost {
			apikit.WriteMethodNotAllowed(w)
			return
		}
		var req apikit.TeamIntentObserverLeaseRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		lease, err := store.ClaimTeamIntentObserverLease(r.Context(), req.ClusterKey, req.ObserverID)
		if err != nil {
			writeTeamIntentError(w, err, "failed to claim observer lease")
			return
		}
		writeJSON(w, http.StatusOK, lease)
		return
	}

	teamID := strings.TrimSpace(parts[0])
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetTeamIntent(r.Context(), teamID)
		if err != nil {
			writeTeamIntentError(w, err, "failed to get team intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "team intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 2 && parts[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.TeamIntentFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimTeamIntentFence(r.Context(), teamID, req.WorkerID)
		if err != nil {
			writeTeamIntentError(w, err, "failed to claim team intent fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(parts) == 2 && parts[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.TeamIntentReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordTeamIntentReceipt(r.Context(), teamID, req)
		if err != nil {
			writeTeamIntentError(w, err, "failed to record team intent receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	case len(parts) == 2 && parts[1] == "observation" && r.Method == http.MethodPut:
		var req apikit.TeamIntentObservationRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		obs, err := store.RecordTeamIntentObservation(r.Context(), teamID, req)
		if err != nil {
			writeTeamIntentError(w, err, "failed to record team intent observation")
			return
		}
		writeJSON(w, http.StatusOK, obs)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func writeTeamIntentError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, teamintent.ErrDisabled):
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "team intent not found")
	case errors.Is(err, teamintent.ErrStaleReceipt), errors.Is(err, teamintent.ErrStaleObservation):
		apikit.WriteEnvelope(w, http.StatusConflict, apikit.CodeConflict, err.Error())
	case errors.Is(err, teamintent.ErrInvalidInput):
		apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, err.Error())
	default:
		apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeQueryFailed, fallback)
	}
}
