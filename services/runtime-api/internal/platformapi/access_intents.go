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
	// ErrAccessIntentDisabled is returned when platform-api's access intent journal is disabled (503).
	ErrAccessIntentDisabled = errors.New("access intent journal disabled")
	// ErrAccessIntentNotFound is returned when an access intent resource does not exist (404).
	ErrAccessIntentNotFound = errors.New("access intent not found")
	// ErrAccessIntentConflict is returned on stale fence/revision/observer epoch or revoked authority (409).
	ErrAccessIntentConflict = errors.New("access intent conflict")
	// ErrAccessIntentInvalid is returned on malformed requests (400).
	ErrAccessIntentInvalid = errors.New("invalid access intent request")
)

func accessIntentError(op string, status int, err error) error {
	switch status {
	case http.StatusServiceUnavailable:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAccessIntentDisabled, err)
		}
		return ErrAccessIntentDisabled
	case http.StatusNotFound:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAccessIntentNotFound, err)
		}
		return ErrAccessIntentNotFound
	case http.StatusConflict:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAccessIntentConflict, err)
		}
		return ErrAccessIntentConflict
	case http.StatusBadRequest:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAccessIntentInvalid, err)
		}
		return ErrAccessIntentInvalid
	default:
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		return fmt.Errorf("%s: status %d", op, status)
	}
}

func (c *Client) ListGrantIntents(ctx context.Context) ([]apikit.GrantIntent, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	var out apikit.GrantIntentListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/access-intents/grants", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, accessIntentError("list grant intents", status, err)
	}
	return out.Grants, nil
}

func (c *Client) UpsertGrantIntent(ctx context.Context, req apikit.GrantIntentUpsertRequest) (apikit.GrantIntent, error) {
	if !c.Configured() {
		return apikit.GrantIntent{}, ErrNotConfigured
	}
	var out apikit.GrantIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/access-intents/grants", req, &out, true)
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		return apikit.GrantIntent{}, accessIntentError("upsert grant intent", status, err)
	}
	return out, nil
}

func (c *Client) GetGrantIntent(ctx context.Context, grantID string) (apikit.GrantIntent, bool, error) {
	if !c.Configured() {
		return apikit.GrantIntent{}, false, ErrNotConfigured
	}
	var out apikit.GrantIntent
	path := "/internal/access-intents/grants/" + url.PathEscape(grantID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.GrantIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.GrantIntent{}, false, accessIntentError("get grant intent", status, err)
	}
	return out, true, nil
}

func (c *Client) RevokeGrantIntent(ctx context.Context, grantID string, deleteGrant bool) (apikit.GrantIntent, error) {
	if !c.Configured() {
		return apikit.GrantIntent{}, ErrNotConfigured
	}
	var out apikit.GrantIntent
	path := "/internal/access-intents/grants/" + url.PathEscape(grantID)
	if deleteGrant {
		path += "?delete=true"
	}
	status, err := c.requestJSON(ctx, http.MethodDelete, path, nil, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.GrantIntent{}, accessIntentError("revoke grant intent", status, err)
	}
	return out, nil
}

func (c *Client) ClaimGrantIntentFence(ctx context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error) {
	if !c.Configured() {
		return apikit.GrantIntentFenceResponse{}, ErrNotConfigured
	}
	var out apikit.GrantIntentFenceResponse
	path := "/internal/access-intents/grants/" + url.PathEscape(grantID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.GrantIntentFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.GrantIntentFenceResponse{}, accessIntentError("claim grant intent fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordGrantIntentReceipt(ctx context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error) {
	if !c.Configured() {
		return apikit.GrantReceipt{}, ErrNotConfigured
	}
	var out apikit.GrantReceipt
	path := "/internal/access-intents/grants/" + url.PathEscape(grantID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.GrantReceipt{}, accessIntentError("record grant intent receipt", status, err)
	}
	return out, nil
}

func (c *Client) RecordGrantIntentObservation(ctx context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error) {
	if !c.Configured() {
		return apikit.GrantObservation{}, ErrNotConfigured
	}
	var out apikit.GrantObservation
	path := "/internal/access-intents/grants/" + url.PathEscape(grantID) + "/observation"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.GrantObservation{}, accessIntentError("record grant intent observation", status, err)
	}
	return out, nil
}

func (c *Client) ListEnrollmentIntents(ctx context.Context) ([]apikit.EnrollmentIntent, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	var out apikit.EnrollmentIntentListResponse
	status, err := c.requestJSON(ctx, http.MethodGet, "/internal/access-intents/enrollments", nil, &out, true)
	if err != nil || status != http.StatusOK {
		return nil, accessIntentError("list enrollment intents", status, err)
	}
	return out.Enrollments, nil
}

func (c *Client) CreateEnrollmentIntent(ctx context.Context, req apikit.EnrollmentIntentCreateRequest) (apikit.EnrollmentIntent, error) {
	if !c.Configured() {
		return apikit.EnrollmentIntent{}, ErrNotConfigured
	}
	var out apikit.EnrollmentIntent
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/access-intents/enrollments", req, &out, true)
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		return apikit.EnrollmentIntent{}, accessIntentError("create enrollment intent", status, err)
	}
	return out, nil
}

func (c *Client) GetEnrollmentIntent(ctx context.Context, opID string) (apikit.EnrollmentIntent, bool, error) {
	if !c.Configured() {
		return apikit.EnrollmentIntent{}, false, ErrNotConfigured
	}
	var out apikit.EnrollmentIntent
	path := "/internal/access-intents/enrollments/" + url.PathEscape(opID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.EnrollmentIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.EnrollmentIntent{}, false, accessIntentError("get enrollment intent", status, err)
	}
	return out, true, nil
}

func (c *Client) ClaimEnrollmentIntentFence(ctx context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error) {
	if !c.Configured() {
		return apikit.EnrollmentIntentFenceResponse{}, ErrNotConfigured
	}
	var out apikit.EnrollmentIntentFenceResponse
	path := "/internal/access-intents/enrollments/" + url.PathEscape(opID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.EnrollmentIntentFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.EnrollmentIntentFenceResponse{}, accessIntentError("claim enrollment intent fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordEnrollmentIntentReceipt(ctx context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error) {
	if !c.Configured() {
		return apikit.EnrollmentReceipt{}, ErrNotConfigured
	}
	var out apikit.EnrollmentReceipt
	path := "/internal/access-intents/enrollments/" + url.PathEscape(opID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.EnrollmentReceipt{}, accessIntentError("record enrollment intent receipt", status, err)
	}
	return out, nil
}

func (c *Client) ActivateEnrollmentIntent(ctx context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error) {
	if !c.Configured() {
		return apikit.EnrollmentIntent{}, ErrNotConfigured
	}
	var out apikit.EnrollmentIntent
	path := "/internal/access-intents/enrollments/" + url.PathEscape(opID) + "/activate"
	status, err := c.requestJSON(ctx, http.MethodPost, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.EnrollmentIntent{}, accessIntentError("activate enrollment intent", status, err)
	}
	return out, nil
}

func (c *Client) GetAgentCleanupIntent(ctx context.Context, agentID string) (apikit.AgentCleanupIntent, bool, error) {
	if !c.Configured() {
		return apikit.AgentCleanupIntent{}, false, ErrNotConfigured
	}
	var out apikit.AgentCleanupIntent
	path := "/internal/access-intents/agent-cleanups/" + url.PathEscape(agentID)
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &out, true)
	if status == http.StatusNotFound {
		return apikit.AgentCleanupIntent{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return apikit.AgentCleanupIntent{}, false, accessIntentError("get agent cleanup intent", status, err)
	}
	return out, true, nil
}

func (c *Client) ClaimAgentCleanupIntentFence(ctx context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error) {
	if !c.Configured() {
		return apikit.AgentCleanupFenceResponse{}, ErrNotConfigured
	}
	var out apikit.AgentCleanupFenceResponse
	path := "/internal/access-intents/agent-cleanups/" + url.PathEscape(agentID) + "/fence"
	status, err := c.requestJSON(ctx, http.MethodPost, path, apikit.AgentCleanupFenceRequest{WorkerID: workerID}, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.AgentCleanupFenceResponse{}, accessIntentError("claim agent cleanup fence", status, err)
	}
	return out, nil
}

func (c *Client) RecordAgentCleanupIntentReceipt(ctx context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error) {
	if !c.Configured() {
		return apikit.AgentCleanupReceipt{}, ErrNotConfigured
	}
	var out apikit.AgentCleanupReceipt
	path := "/internal/access-intents/agent-cleanups/" + url.PathEscape(agentID) + "/receipt"
	status, err := c.requestJSON(ctx, http.MethodPut, path, req, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.AgentCleanupReceipt{}, accessIntentError("record agent cleanup receipt", status, err)
	}
	return out, nil
}

func (c *Client) ClaimAccessIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error) {
	if !c.Configured() {
		return apikit.AccessIntentObserverLeaseResponse{}, ErrNotConfigured
	}
	var out apikit.AccessIntentObserverLeaseResponse
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/access-intents/observer-lease", apikit.AccessIntentObserverLeaseRequest{
		ClusterKey: clusterKey,
		ObserverID: observerID,
	}, &out, true)
	if err != nil || status != http.StatusOK {
		return apikit.AccessIntentObserverLeaseResponse{}, accessIntentError("claim access intent observer lease", status, err)
	}
	return out, nil
}
