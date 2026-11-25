package platforminternal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"mcp-platform-api/internal/workloadintent"
	"mcp-runtime/pkg/apikit"
)

// WorkloadIntentStore is implemented by PlatformStore when durable workload
// intent persistence is available.
type WorkloadIntentStore interface {
	ClaimWorkloadObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error)

	ListServerIntents(ctx context.Context) ([]apikit.ServerIntent, error)
	GetServerIntent(ctx context.Context, serverID string) (apikit.ServerIntent, bool, error)
	UpsertServerIntent(ctx context.Context, req apikit.ServerIntentUpsertRequest) (apikit.ServerIntent, error)
	RetireServerIntent(ctx context.Context, serverID string, req apikit.ServerIntentRetireRequest) (apikit.ServerIntent, error)
	ClaimServerIntentFence(ctx context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error)
	RecordServerRetirementCheckpoint(ctx context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error)
	RecordServerIntentReceipt(ctx context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
	RecordServerIntentObservation(ctx context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error)

	ListDeploymentIntents(ctx context.Context) ([]apikit.DeploymentIntent, error)
	GetDeploymentIntent(ctx context.Context, deploymentID string) (apikit.DeploymentIntent, bool, error)
	UpsertDeploymentIntent(ctx context.Context, req apikit.DeploymentIntentUpsertRequest) (apikit.DeploymentIntent, error)
	DeleteDeploymentIntent(ctx context.Context, deploymentID string, req apikit.DeploymentIntentDeleteRequest) (apikit.DeploymentIntent, error)
	ClaimDeploymentIntentFence(ctx context.Context, deploymentID, workerID string) (apikit.DeploymentIntentFenceResponse, error)
	RecordDeploymentIntentReceipt(ctx context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
	RecordDeploymentIntentObservation(ctx context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error)

	ListImagePublicationIntents(ctx context.Context) ([]apikit.ImagePublicationIntent, error)
	GetImagePublicationIntent(ctx context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error)
	BeginImagePublication(ctx context.Context, req apikit.ImagePublicationBeginRequest) (apikit.ImagePublicationIntent, error)
	FinalizeImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationFinalizeRequest) (apikit.ImagePublicationIntent, error)
	AbortImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationAbortRequest) (apikit.ImagePublicationIntent, error)
	ClaimImagePublicationFence(ctx context.Context, publicationID, workerID string) (apikit.ImagePublicationFenceResponse, error)
	RecordImagePublicationReceipt(ctx context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error)

	ListRestartOperationIntents(ctx context.Context) ([]apikit.RestartOperationIntent, error)
	GetRestartOperationIntent(ctx context.Context, operationID string) (apikit.RestartOperationIntent, bool, error)
	CreateRestartOperation(ctx context.Context, req apikit.RestartOperationCreateRequest) (apikit.RestartOperationIntent, error)
	ClaimRestartOperationFence(ctx context.Context, operationID, workerID string) (apikit.RestartOperationFenceResponse, error)
	RecordRestartComponentCheckpoint(ctx context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error)
	RecordRestartOperationReceipt(ctx context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
}

func (h Handler) workloadIntentStore() (WorkloadIntentStore, bool) {
	store, ok := h.Store.(WorkloadIntentStore)
	return store, ok
}

func (h Handler) workloadIntentPath(w http.ResponseWriter, r *http.Request) {
	store, ok := h.workloadIntentStore()
	if !ok {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, workloadintent.ErrDisabled.Error())
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/workload-intents/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "workload intent resource not found")
		return
	}
	switch parts[0] {
	case "observer-lease":
		h.handleWorkloadObserverLease(w, r, store, parts)
	case "servers":
		h.handleServerIntents(w, r, store, parts[1:])
	case "deployments":
		h.handleDeploymentIntents(w, r, store, parts[1:])
	case "publications":
		h.handlePublicationIntents(w, r, store, parts[1:])
	case "restarts":
		h.handleRestartIntents(w, r, store, parts[1:])
	default:
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "workload intent resource not found")
	}
}

func (h Handler) handleWorkloadObserverLease(w http.ResponseWriter, r *http.Request, store WorkloadIntentStore, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodPost {
		apikit.WriteMethodNotAllowed(w)
		return
	}
	var req apikit.WorkloadObserverLeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
		return
	}
	lease, err := store.ClaimWorkloadObserverLease(r.Context(), req.ClusterKey, req.ObserverID)
	if err != nil {
		writeWorkloadIntentError(w, err, "failed to claim observer lease")
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (h Handler) handleServerIntents(w http.ResponseWriter, r *http.Request, store WorkloadIntentStore, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		items, err := store.ListServerIntents(r.Context())
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to list server intents")
			return
		}
		writeJSON(w, http.StatusOK, apikit.ServerIntentListResponse{Intents: items})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var req apikit.ServerIntentUpsertRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.UpsertServerIntent(r.Context(), req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to upsert server intent")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	case len(rest) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetServerIntent(r.Context(), rest[0])
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to get server intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "server intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "retire" && r.Method == http.MethodPost:
		var req apikit.ServerIntentRetireRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
		}
		item, err := store.RetireServerIntent(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to retire server intent")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	case len(rest) == 2 && rest[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.ServerIntentFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimServerIntentFence(r.Context(), rest[0], req.WorkerID)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to claim server intent fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(rest) == 2 && rest[1] == "checkpoint" && r.Method == http.MethodPut:
		var req apikit.ServerRetirementCheckpointRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.RecordServerRetirementCheckpoint(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record server retirement checkpoint")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.ServerIntentReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordServerIntentReceipt(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record server intent receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	case len(rest) == 2 && rest[1] == "observation" && r.Method == http.MethodPut:
		var req apikit.ServerIntentObservationRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		obs, err := store.RecordServerIntentObservation(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record server intent observation")
			return
		}
		writeJSON(w, http.StatusOK, obs)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func (h Handler) handleDeploymentIntents(w http.ResponseWriter, r *http.Request, store WorkloadIntentStore, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		items, err := store.ListDeploymentIntents(r.Context())
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to list deployment intents")
			return
		}
		writeJSON(w, http.StatusOK, apikit.DeploymentIntentListResponse{Intents: items})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var req apikit.DeploymentIntentUpsertRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.UpsertDeploymentIntent(r.Context(), req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to upsert deployment intent")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	case len(rest) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetDeploymentIntent(r.Context(), rest[0])
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to get deployment intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "deployment intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "delete" && r.Method == http.MethodPost:
		var req apikit.DeploymentIntentDeleteRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
		}
		item, err := store.DeleteDeploymentIntent(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to delete deployment intent")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	case len(rest) == 2 && rest[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.DeploymentIntentFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimDeploymentIntentFence(r.Context(), rest[0], req.WorkerID)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to claim deployment intent fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(rest) == 2 && rest[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.DeploymentIntentReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordDeploymentIntentReceipt(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record deployment intent receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	case len(rest) == 2 && rest[1] == "observation" && r.Method == http.MethodPut:
		var req apikit.DeploymentIntentObservationRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		obs, err := store.RecordDeploymentIntentObservation(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record deployment intent observation")
			return
		}
		writeJSON(w, http.StatusOK, obs)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func (h Handler) handlePublicationIntents(w http.ResponseWriter, r *http.Request, store WorkloadIntentStore, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		items, err := store.ListImagePublicationIntents(r.Context())
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to list image publication intents")
			return
		}
		writeJSON(w, http.StatusOK, apikit.ImagePublicationListResponse{Intents: items})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var req apikit.ImagePublicationBeginRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.BeginImagePublication(r.Context(), req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to begin image publication")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	case len(rest) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetImagePublicationIntent(r.Context(), rest[0])
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to get image publication intent")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "image publication intent not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "finalize" && r.Method == http.MethodPost:
		var req apikit.ImagePublicationFinalizeRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.FinalizeImagePublication(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to finalize image publication")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	case len(rest) == 2 && rest[1] == "abort" && r.Method == http.MethodPost:
		var req apikit.ImagePublicationAbortRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
				return
			}
		}
		item, err := store.AbortImagePublication(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to abort image publication")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.ImagePublicationFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimImagePublicationFence(r.Context(), rest[0], req.WorkerID)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to claim image publication fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(rest) == 2 && rest[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.ImagePublicationReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordImagePublicationReceipt(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record image publication receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func (h Handler) handleRestartIntents(w http.ResponseWriter, r *http.Request, store WorkloadIntentStore, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		items, err := store.ListRestartOperationIntents(r.Context())
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to list restart operations")
			return
		}
		writeJSON(w, http.StatusOK, apikit.RestartOperationListResponse{Intents: items})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var req apikit.RestartOperationCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.CreateRestartOperation(r.Context(), req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to create restart operation")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	case len(rest) == 1 && r.Method == http.MethodGet:
		item, found, err := store.GetRestartOperationIntent(r.Context(), rest[0])
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to get restart operation")
			return
		}
		if !found {
			apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "restart operation not found")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "fence" && r.Method == http.MethodPost:
		var req apikit.RestartOperationFenceRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		resp, err := store.ClaimRestartOperationFence(r.Context(), rest[0], req.WorkerID)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to claim restart operation fence")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case len(rest) == 2 && rest[1] == "checkpoint" && r.Method == http.MethodPut:
		var req apikit.RestartComponentCheckpointRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := store.RecordRestartComponentCheckpoint(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record restart component checkpoint")
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(rest) == 2 && rest[1] == "receipt" && r.Method == http.MethodPut:
		var req apikit.RestartOperationReceiptRequest
		if err := decodeJSON(r, &req); err != nil {
			apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, "invalid request body")
			return
		}
		receipt, err := store.RecordRestartOperationReceipt(r.Context(), rest[0], req)
		if err != nil {
			writeWorkloadIntentError(w, err, "failed to record restart operation receipt")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	default:
		apikit.WriteMethodNotAllowed(w)
	}
}

func writeWorkloadIntentError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, workloadintent.ErrDisabled):
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, "workload intent not found")
	case errors.Is(err, workloadintent.ErrStaleReceipt),
		errors.Is(err, workloadintent.ErrStaleObservation),
		errors.Is(err, workloadintent.ErrTagOverlapConflict),
		errors.Is(err, workloadintent.ErrInvalidStateTransition):
		apikit.WriteEnvelope(w, http.StatusConflict, apikit.CodeConflict, err.Error())
	case errors.Is(err, workloadintent.ErrInvalidInput):
		apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, err.Error())
	default:
		apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeQueryFailed, fallback)
	}
}
