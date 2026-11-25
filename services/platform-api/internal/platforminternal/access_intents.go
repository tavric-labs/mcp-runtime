package platforminternal

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mcp-platform-api/internal/accessintent"
	"mcp-runtime/pkg/apikit"
)

// AccessIntentStore is implemented by PlatformStore when durable access-control
// intent persistence is available.
type AccessIntentStore interface {
	ListGrantIntents(ctx context.Context) ([]apikit.GrantIntent, error)
	UpsertGrantIntent(ctx context.Context, req apikit.GrantIntentUpsertRequest) (apikit.GrantIntent, error)
	GetGrantIntent(ctx context.Context, grantID string) (apikit.GrantIntent, bool, error)
	RevokeGrantIntent(ctx context.Context, grantID string, deleteGrant bool) (apikit.GrantIntent, error)
	ClaimGrantIntentFence(ctx context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error)
	RecordGrantIntentReceipt(ctx context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error)
	RecordGrantIntentObservation(ctx context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error)

	ListEnrollmentIntents(ctx context.Context) ([]apikit.EnrollmentIntent, error)
	CreateEnrollmentIntent(ctx context.Context, req apikit.EnrollmentIntentCreateRequest) (apikit.EnrollmentIntent, bool, error)
	GetEnrollmentIntent(ctx context.Context, opID string) (apikit.EnrollmentIntent, bool, error)
	ClaimEnrollmentIntentFence(ctx context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error)
	RecordEnrollmentIntentReceipt(ctx context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error)
	ActivateEnrollmentIntent(ctx context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error)

	GetAgentCleanupIntent(ctx context.Context, agentID string) (apikit.AgentCleanupIntent, bool, error)
	ClaimAgentCleanupIntentFence(ctx context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error)
	RecordAgentCleanupIntentReceipt(ctx context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error)

	ClaimAccessIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error)
}

func (h Handler) accessIntentStore() (AccessIntentStore, bool) {
	store, ok := h.Store.(AccessIntentStore)
	return store, ok
}

func (h Handler) accessIntentPath(w http.ResponseWriter, r *http.Request) {
	store, ok := h.accessIntentStore()
	if !ok {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, accessintent.ErrDisabled.Error())
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/access-intents/"), "/")
	if path == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "access intent endpoint not found")
		return
	}
	parts := strings.Split(path, "/")
	switch parts[0] {
	case "observer-lease":
		h.accessIntentObserverLease(w, r, store, parts)
	case "grants":
		h.accessIntentGrants(w, r, store, parts[1:])
	case "enrollments":
		h.accessIntentEnrollments(w, r, store, parts[1:])
	case "agent-cleanups":
		h.accessIntentAgentCleanups(w, r, store, parts[1:])
	default:
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "access intent endpoint not found")
	}
}

func (h Handler) accessIntentObserverLease(w http.ResponseWriter, r *http.Request, store AccessIntentStore, parts []string) {
	if len(parts) != 1 {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "observer lease endpoint not found")
		return
	}
	if r.Method != http.MethodPost {
		apikit.WriteMethodNotAllowed(w)
		return
	}
	var req apikit.AccessIntentObserverLeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
		return
	}
	lease, err := store.ClaimAccessIntentObserverLease(r.Context(), req.ClusterKey, req.ObserverID)
	if err != nil {
		writeAccessIntentError(w, err, "failed to claim observer lease")
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (h Handler) accessIntentGrants(w http.ResponseWriter, r *http.Request, store AccessIntentStore, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := store.ListGrantIntents(r.Context())
			if err != nil {
				writeAccessIntentError(w, err, "failed to list grant intents")
				return
			}
			writeJSON(w, http.StatusOK, apikit.GrantIntentListResponse{Grants: items})
		case http.MethodPost:
			var req apikit.GrantIntentUpsertRequest
			if err := decodeJSON(r, &req); err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
			item, err := store.UpsertGrantIntent(r.Context(), req)
			if err != nil {
				writeAccessIntentError(w, err, "failed to upsert grant intent")
				return
			}
			writeJSON(w, http.StatusCreated, item)
		default:
			apikit.WriteMethodNotAllowed(w)
		}
		return
	}

	grantID := strings.TrimSpace(parts[0])
	if grantID == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "grant intent not found")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetGrantIntent(r.Context(), grantID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to get grant intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "grant intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		deleteGrant := false
		if raw := strings.TrimSpace(r.URL.Query().Get("delete")); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidQueryParam, "delete must be a boolean")
				return
			}
			deleteGrant = parsed
		}
		if r.Body != nil {
			var req apikit.GrantIntentRevokeRequest
			if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
			if req.Delete {
				deleteGrant = true
			}
		}
		item, err := store.RevokeGrantIntent(r.Context(), grantID, deleteGrant)
		if err != nil {
			writeAccessIntentError(w, err, "failed to revoke grant intent")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 2 && parts[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.GrantIntentFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimGrantIntentFence(r.Context(), grantID, req.WorkerID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to claim grant intent fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(parts) == 2 && parts[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.GrantIntentReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordGrantIntentReceipt(r.Context(), grantID, req)
		if err != nil {
			writeAccessIntentError(w, err, "failed to record grant intent receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	case len(parts) == 2 && parts[1] == "observation" && r.Method == http.MethodPut:
		var req apikit.GrantIntentObservationRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		obs, err := store.RecordGrantIntentObservation(r.Context(), grantID, req)
		if err != nil {
			writeAccessIntentError(w, err, "failed to record grant intent observation")
			return
		}
		writeJSON(w, http.StatusOK, obs)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func (h Handler) accessIntentEnrollments(w http.ResponseWriter, r *http.Request, store AccessIntentStore, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := store.ListEnrollmentIntents(r.Context())
			if err != nil {
				writeAccessIntentError(w, err, "failed to list enrollment intents")
				return
			}
			writeJSON(w, http.StatusOK, apikit.EnrollmentIntentListResponse{Enrollments: items})
		case http.MethodPost:
			var req apikit.EnrollmentIntentCreateRequest
			if err := decodeJSON(r, &req); err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
			item, created, err := store.CreateEnrollmentIntent(r.Context(), req)
			if err != nil {
				writeAccessIntentError(w, err, "failed to create enrollment intent")
				return
			}
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, item)
		default:
			apikit.WriteMethodNotAllowed(w)
		}
		return
	}

	opID := strings.TrimSpace(parts[0])
	if opID == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "enrollment intent not found")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetEnrollmentIntent(r.Context(), opID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to get enrollment intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "enrollment intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 2 && parts[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.EnrollmentIntentFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimEnrollmentIntentFence(r.Context(), opID, req.WorkerID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to claim enrollment fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(parts) == 2 && parts[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.EnrollmentIntentReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordEnrollmentIntentReceipt(r.Context(), opID, req)
		if err != nil {
			writeAccessIntentError(w, err, "failed to record enrollment receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	case len(parts) == 2 && parts[1] == "activate" && r.Method == http.MethodPost:
		var req apikit.EnrollmentIntentActivateRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.ActivateEnrollmentIntent(r.Context(), opID, req)
		if err != nil {
			writeAccessIntentError(w, err, "failed to activate enrollment")
			return
		}
		writeJSON(w, http.StatusOK, item)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func (h Handler) accessIntentAgentCleanups(w http.ResponseWriter, r *http.Request, store AccessIntentStore, parts []string) {
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "agent cleanup intent not found")
		return
	}
	agentID := strings.TrimSpace(parts[0])
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetAgentCleanupIntent(r.Context(), agentID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to get agent cleanup intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "agent cleanup intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 2 && parts[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.AgentCleanupFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimAgentCleanupIntentFence(r.Context(), agentID, req.WorkerID)
		if err != nil {
			writeAccessIntentError(w, err, "failed to claim agent cleanup fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(parts) == 2 && parts[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.AgentCleanupReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordAgentCleanupIntentReceipt(r.Context(), agentID, req)
		if err != nil {
			writeAccessIntentError(w, err, "failed to record agent cleanup receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func writeAccessIntentError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, accessintent.ErrDisabled):
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "access intent not found")
	case errors.Is(err, accessintent.ErrStaleReceipt),
		errors.Is(err, accessintent.ErrStaleObservation),
		errors.Is(err, accessintent.ErrAuthorityRevoked):
		apikit.WriteEnvelope(w, http.StatusConflict, apikit.CodeConflict, err.Error())
	case errors.Is(err, accessintent.ErrInvalidInput):
		apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, err.Error())
	default:
		apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeQueryFailed, fallback)
	}
}
