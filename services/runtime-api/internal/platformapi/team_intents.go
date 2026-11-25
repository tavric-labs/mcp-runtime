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
	// ErrTeamIntentDisabled is returned when PLATFORM_TEAM_INTENT_JOURNAL is off.
	ErrTeamIntentDisabled = errors.New("team intent journal disabled")
	// ErrTeamIntentConflict is returned when a receipt or observation is
	// rejected due to a stale fence, revision, observer epoch, or ownership mismatch.
	ErrTeamIntentConflict = errors.New("team intent conflict")
)

func (c *Client) ListTeamIntents(ctx context.Context) ([]apikit.TeamNamespaceIntent, error) {
	var resp apikit.TeamNamespaceIntentListResponse
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/team-intents", nil, &resp)
	if err != nil {
		return nil, err
	}
	if status == http.StatusServiceUnavailable {
		return nil, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list team intents: status %d", status)
	}
	return resp.Intents, nil
}

func (c *Client) GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	var item apikit.TeamNamespaceIntent
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/team-intents/"+url.PathEscape(teamID), nil, &item)
	if err != nil {
		return apikit.TeamNamespaceIntent{}, false, err
	}
	if status == http.StatusNotFound {
		return apikit.TeamNamespaceIntent{}, false, nil
	}
	if status == http.StatusServiceUnavailable {
		return apikit.TeamNamespaceIntent{}, false, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return apikit.TeamNamespaceIntent{}, false, fmt.Errorf("get team intent: status %d", status)
	}
	return item, true, nil
}

func (c *Client) ClaimTeamIntentFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error) {
	var resp apikit.TeamIntentFenceResponse
	path := "/internal/team-intents/" + url.PathEscape(teamID) + "/fence"
	status, err := c.authorizedJSON(ctx, http.MethodPost, path, apikit.TeamIntentFenceRequest{WorkerID: workerID}, &resp)
	if err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	if status == http.StatusNotFound {
		return apikit.TeamIntentFenceResponse{}, ErrTeamNotFound
	}
	if status == http.StatusServiceUnavailable {
		return apikit.TeamIntentFenceResponse{}, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return apikit.TeamIntentFenceResponse{}, fmt.Errorf("claim team intent fence: status %d", status)
	}
	return resp, nil
}

func (c *Client) RecordTeamIntentReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error) {
	var resp apikit.TeamNamespaceReceipt
	path := "/internal/team-intents/" + url.PathEscape(teamID) + "/receipt"
	status, err := c.authorizedJSON(ctx, http.MethodPut, path, req, &resp)
	if err != nil {
		return apikit.TeamNamespaceReceipt{}, err
	}
	if status == http.StatusConflict {
		return apikit.TeamNamespaceReceipt{}, ErrTeamIntentConflict
	}
	if status == http.StatusNotFound {
		return apikit.TeamNamespaceReceipt{}, ErrTeamNotFound
	}
	if status == http.StatusServiceUnavailable {
		return apikit.TeamNamespaceReceipt{}, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return apikit.TeamNamespaceReceipt{}, fmt.Errorf("record team intent receipt: status %d", status)
	}
	return resp, nil
}

func (c *Client) ClaimTeamIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error) {
	var resp apikit.TeamIntentObserverLeaseResponse
	status, err := c.authorizedJSON(ctx, http.MethodPost, "/internal/team-intents/observer-lease", apikit.TeamIntentObserverLeaseRequest{
		ClusterKey: clusterKey,
		ObserverID: observerID,
	}, &resp)
	if err != nil {
		return apikit.TeamIntentObserverLeaseResponse{}, err
	}
	if status == http.StatusServiceUnavailable {
		return apikit.TeamIntentObserverLeaseResponse{}, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return apikit.TeamIntentObserverLeaseResponse{}, fmt.Errorf("claim team intent observer lease: status %d", status)
	}
	return resp, nil
}

func (c *Client) RecordTeamIntentObservation(ctx context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error) {
	var resp apikit.TeamNamespaceObservation
	path := "/internal/team-intents/" + url.PathEscape(teamID) + "/observation"
	status, err := c.authorizedJSON(ctx, http.MethodPut, path, req, &resp)
	if err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}
	if status == http.StatusConflict {
		return apikit.TeamNamespaceObservation{}, ErrTeamIntentConflict
	}
	if status == http.StatusNotFound {
		return apikit.TeamNamespaceObservation{}, ErrTeamNotFound
	}
	if status == http.StatusServiceUnavailable {
		return apikit.TeamNamespaceObservation{}, ErrTeamIntentDisabled
	}
	if status != http.StatusOK {
		return apikit.TeamNamespaceObservation{}, fmt.Errorf("record team intent observation: status %d", status)
	}
	return resp, nil
}
