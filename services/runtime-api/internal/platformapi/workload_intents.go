package platformapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"mcp-runtime/pkg/apikit"
)

var (
	// ErrWorkloadIntentDisabled indicates PLATFORM_WORKLOAD_INTENT_JOURNAL is disabled.
	ErrWorkloadIntentDisabled = errors.New("workload intent journal disabled")
	// ErrWorkloadIntentNotFound indicates the requested workload intent does not exist.
	ErrWorkloadIntentNotFound = errors.New("workload intent not found")
	// ErrWorkloadIntentConflict indicates a stale fence, revision, observer epoch,
	// state transition, or mutable-tag overlap conflict (HTTP 409).
	ErrWorkloadIntentConflict = errors.New("workload intent conflict")
)

func workloadIntentError(action string, status int, err error) error {
	switch status {
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%s: %w", action, ErrWorkloadIntentDisabled)
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", action, ErrWorkloadIntentNotFound)
	case http.StatusConflict:
		if err != nil {
			return fmt.Errorf("%s: %w: %v", action, ErrWorkloadIntentConflict, err)
		}
		return fmt.Errorf("%s: %w", action, ErrWorkloadIntentConflict)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%s: status %d", action, status)
}

func (c *Client) ClaimWorkloadObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error) {
	var out apikit.WorkloadObserverLeaseResponse
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/workload-intents/observer-lease", apikit.WorkloadObserverLeaseRequest{
		ClusterKey: clusterKey,
		ObserverID: observerID,
	}, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("claim workload observer lease", status, err)
	}
	return out, nil
}

func (c *Client) ListServerIntents(ctx context.Context) ([]apikit.ServerIntent, error) {
	var out apikit.ServerIntentListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/workload-intents/servers", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, workloadIntentError("list server intents", status, err)
	}
	return out.Intents, nil
}

func (c *Client) UpsertServerIntent(ctx context.Context, req apikit.ServerIntentUpsertRequest) (apikit.ServerIntent, error) {
	var out apikit.ServerIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/workload-intents/servers", req, &out, true)
	if err != nil || (status != http.StatusAccepted && status != http.StatusOK) {
		return out, workloadIntentError("upsert server intent", status, err)
	}
	return out, nil
}

func (c *Client) GetServerIntent(ctx context.Context, serverID string) (apikit.ServerIntent, bool, error) {
	var out apikit.ServerIntent
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.ServerIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.ServerIntent{}, false, workloadIntentError("get server intent", status, err)
	}
	return out, true, nil
}

func (c *Client) RetireServerIntent(ctx context.Context, serverID string, req apikit.ServerIntentRetireRequest) (apikit.ServerIntent, error) {
	var out apikit.ServerIntent
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID) + "/retire"
	status, err := c.requestJSON(ctx, http.MethodPost, path, req, &out, true)
	if err != nil || (status != http.StatusAccepted && status != http.StatusOK) {
		return out, workloadIntentError("retire server intent", status, err)
	}
	return out, nil
}

func (c *Client) ClaimServerIntentFence(ctx context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error) {
	var out apikit.ServerIntentFenceResponse
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.ServerIntentFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("claim server intent fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordServerRetirementCheckpoint(ctx context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error) {
	var out apikit.ServerIntent
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID) + "/checkpoint"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record server retirement checkpoint", status, err)
	}
	return out, nil
}

func (c *Client) RecordServerIntentReceipt(ctx context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	var out apikit.WorkloadExecutionReceipt
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record server intent receipt", status, err)
	}
	return out, nil
}

func (c *Client) RecordServerIntentObservation(ctx context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	var out apikit.WorkloadStatusObservation
	path := "/internal/workload-intents/servers/" + url.PathEscape(serverID) + "/observation"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record server intent observation", status, err)
	}
	return out, nil
}

func (c *Client) ListDeploymentIntents(ctx context.Context) ([]apikit.DeploymentIntent, error) {
	var out apikit.DeploymentIntentListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/workload-intents/deployments", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, workloadIntentError("list deployment intents", status, err)
	}
	return out.Intents, nil
}

func (c *Client) UpsertDeploymentIntent(ctx context.Context, req apikit.DeploymentIntentUpsertRequest) (apikit.DeploymentIntent, error) {
	var out apikit.DeploymentIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/workload-intents/deployments", req, &out, true)
	if err != nil || (status != http.StatusAccepted && status != http.StatusOK) {
		return out, workloadIntentError("upsert deployment intent", status, err)
	}
	return out, nil
}

func (c *Client) GetDeploymentIntent(ctx context.Context, deploymentID string) (apikit.DeploymentIntent, bool, error) {
	var out apikit.DeploymentIntent
	path := "/internal/workload-intents/deployments/" + url.PathEscape(deploymentID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.DeploymentIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.DeploymentIntent{}, false, workloadIntentError("get deployment intent", status, err)
	}
	return out, true, nil
}

func (c *Client) DeleteDeploymentIntent(ctx context.Context, deploymentID string, req apikit.DeploymentIntentDeleteRequest) (apikit.DeploymentIntent, error) {
	var out apikit.DeploymentIntent
	path := "/internal/workload-intents/deployments/" + url.PathEscape(deploymentID) + "/delete"
	status, err := c.requestJSON(ctx, http.MethodPost, path, req, &out, true)
	if err != nil || (status != http.StatusAccepted && status != http.StatusOK) {
		return out, workloadIntentError("delete deployment intent", status, err)
	}
	return out, nil
}

func (c *Client) ClaimDeploymentIntentFence(ctx context.Context, deploymentID, workerID string) (apikit.DeploymentIntentFenceResponse, error) {
	var out apikit.DeploymentIntentFenceResponse
	path := "/internal/workload-intents/deployments/" + url.PathEscape(deploymentID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.DeploymentIntentFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("claim deployment intent fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordDeploymentIntentReceipt(ctx context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	var out apikit.WorkloadExecutionReceipt
	path := "/internal/workload-intents/deployments/" + url.PathEscape(deploymentID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record deployment intent receipt", status, err)
	}
	return out, nil
}

func (c *Client) RecordDeploymentIntentObservation(ctx context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	var out apikit.WorkloadStatusObservation
	path := "/internal/workload-intents/deployments/" + url.PathEscape(deploymentID) + "/observation"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record deployment intent observation", status, err)
	}
	return out, nil
}

func (c *Client) ListImagePublicationIntents(ctx context.Context) ([]apikit.ImagePublicationIntent, error) {
	var out apikit.ImagePublicationListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/workload-intents/publications", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, workloadIntentError("list image publication intents", status, err)
	}
	return out.Intents, nil
}

func (c *Client) BeginImagePublication(ctx context.Context, req apikit.ImagePublicationBeginRequest) (apikit.ImagePublicationIntent, error) {
	var out apikit.ImagePublicationIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/workload-intents/publications", req, &out, true)
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		return out, workloadIntentError("begin image publication", status, err)
	}
	return out, nil
}

func (c *Client) GetImagePublicationIntent(ctx context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error) {
	var out apikit.ImagePublicationIntent
	path := "/internal/workload-intents/publications/" + url.PathEscape(publicationID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.ImagePublicationIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.ImagePublicationIntent{}, false, workloadIntentError("get image publication intent", status, err)
	}
	return out, true, nil
}

func (c *Client) FinalizeImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationFinalizeRequest) (apikit.ImagePublicationIntent, error) {
	var out apikit.ImagePublicationIntent
	path := "/internal/workload-intents/publications/" + url.PathEscape(publicationID) + "/finalize"
	status, err := c.requestJSON(ctx, http.MethodPost, path, req, &out, true)
	if err != nil || (status != http.StatusAccepted && status != http.StatusOK) {
		return out, workloadIntentError("finalize image publication", status, err)
	}
	return out, nil
}

func (c *Client) AbortImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationAbortRequest) (apikit.ImagePublicationIntent, error) {
	var out apikit.ImagePublicationIntent
	path := "/internal/workload-intents/publications/" + url.PathEscape(publicationID) + "/abort"
	status, err := c.requestJSON(ctx, http.MethodPost, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("abort image publication", status, err)
	}
	return out, nil
}

func (c *Client) ClaimImagePublicationFence(ctx context.Context, publicationID, workerID string) (apikit.ImagePublicationFenceResponse, error) {
	var out apikit.ImagePublicationFenceResponse
	path := "/internal/workload-intents/publications/" + url.PathEscape(publicationID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.ImagePublicationFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("claim image publication fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordImagePublicationReceipt(ctx context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	var out apikit.WorkloadExecutionReceipt
	path := "/internal/workload-intents/publications/" + url.PathEscape(publicationID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record image publication receipt", status, err)
	}
	return out, nil
}

func (c *Client) ListRestartOperationIntents(ctx context.Context) ([]apikit.RestartOperationIntent, error) {
	var out apikit.RestartOperationListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/workload-intents/restarts", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, workloadIntentError("list restart operation intents", status, err)
	}
	return out.Intents, nil
}

func (c *Client) CreateRestartOperation(ctx context.Context, req apikit.RestartOperationCreateRequest) (apikit.RestartOperationIntent, error) {
	var out apikit.RestartOperationIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/workload-intents/restarts", req, &out, true)
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		return out, workloadIntentError("create restart operation", status, err)
	}
	return out, nil
}

func (c *Client) GetRestartOperationIntent(ctx context.Context, operationID string) (apikit.RestartOperationIntent, bool, error) {
	var out apikit.RestartOperationIntent
	path := "/internal/workload-intents/restarts/" + url.PathEscape(operationID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.RestartOperationIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.RestartOperationIntent{}, false, workloadIntentError("get restart operation intent", status, err)
	}
	return out, true, nil
}

func (c *Client) ClaimRestartOperationFence(ctx context.Context, operationID, workerID string) (apikit.RestartOperationFenceResponse, error) {
	var out apikit.RestartOperationFenceResponse
	path := "/internal/workload-intents/restarts/" + url.PathEscape(operationID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.RestartOperationFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("claim restart operation fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordRestartComponentCheckpoint(ctx context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error) {
	var out apikit.RestartOperationIntent
	path := "/internal/workload-intents/restarts/" + url.PathEscape(operationID) + "/checkpoint"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record restart component checkpoint", status, err)
	}
	return out, nil
}

func (c *Client) RecordRestartOperationReceipt(ctx context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	var out apikit.WorkloadExecutionReceipt
	path := "/internal/workload-intents/restarts/" + url.PathEscape(operationID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return out, workloadIntentError("record restart operation receipt", status, err)
	}
	return out, nil
}
