package platforminternal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
)

type teamIntentFakeStore struct {
	fakeStore
	intents        map[string]apikit.TeamNamespaceIntent
	fenceEpochs    map[string]int64
	observerEpochs map[string]int64
	disabled       bool
	receiptErr     error
	observationErr error
}

func (f *teamIntentFakeStore) ListTeamIntents(context.Context) ([]apikit.TeamNamespaceIntent, error) {
	if f.disabled {
		return nil, teamintent.ErrDisabled
	}
	out := make([]apikit.TeamNamespaceIntent, 0, len(f.intents))
	for _, item := range f.intents {
		out = append(out, item)
	}
	return out, nil
}

func (f *teamIntentFakeStore) GetTeamIntent(_ context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	if f.disabled {
		return apikit.TeamNamespaceIntent{}, false, teamintent.ErrDisabled
	}
	item, ok := f.intents[teamID]
	return item, ok, nil
}

func (f *teamIntentFakeStore) ClaimTeamIntentFence(_ context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error) {
	if f.disabled {
		return apikit.TeamIntentFenceResponse{}, teamintent.ErrDisabled
	}
	item, ok := f.intents[teamID]
	if !ok {
		return apikit.TeamIntentFenceResponse{}, sql.ErrNoRows
	}
	if f.fenceEpochs == nil {
		f.fenceEpochs = map[string]int64{}
	}
	f.fenceEpochs[teamID]++
	epoch := f.fenceEpochs[teamID]
	item.Receipt = &apikit.TeamNamespaceReceipt{
		FenceEpoch:  epoch,
		FenceHolder: workerID,
		UpdatedAt:   time.Now().UTC(),
	}
	f.intents[teamID] = item
	return apikit.TeamIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

func (f *teamIntentFakeStore) RecordTeamIntentReceipt(_ context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error) {
	if f.disabled {
		return apikit.TeamNamespaceReceipt{}, teamintent.ErrDisabled
	}
	if f.receiptErr != nil {
		return apikit.TeamNamespaceReceipt{}, f.receiptErr
	}
	if _, ok := f.intents[teamID]; !ok {
		return apikit.TeamNamespaceReceipt{}, sql.ErrNoRows
	}
	if req.FenceEpoch != f.fenceEpochs[teamID] {
		return apikit.TeamNamespaceReceipt{}, teamintent.ErrStaleReceipt
	}
	now := time.Now().UTC()
	receipt := apikit.TeamNamespaceReceipt{
		FenceEpoch:        req.FenceEpoch,
		FenceHolder:       req.WorkerID,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		NamespaceUID:      req.NamespaceUID,
		Attempt:           req.Attempt,
		WorkerID:          req.WorkerID,
		LastErrorClass:    req.LastErrorClass,
		LastError:         req.LastError,
		AppliedAt:         &now,
		UpdatedAt:         now,
	}
	item := f.intents[teamID]
	item.Receipt = &receipt
	f.intents[teamID] = item
	return receipt, nil
}

func (f *teamIntentFakeStore) ClaimTeamIntentObserverLease(_ context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error) {
	if f.disabled {
		return apikit.TeamIntentObserverLeaseResponse{}, teamintent.ErrDisabled
	}
	if clusterKey == "" {
		clusterKey = "default"
	}
	if f.observerEpochs == nil {
		f.observerEpochs = map[string]int64{}
	}
	f.observerEpochs[clusterKey]++
	return apikit.TeamIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: f.observerEpochs[clusterKey],
	}, nil
}

func (f *teamIntentFakeStore) RecordTeamIntentObservation(_ context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error) {
	if f.disabled {
		return apikit.TeamNamespaceObservation{}, teamintent.ErrDisabled
	}
	if f.observationErr != nil {
		return apikit.TeamNamespaceObservation{}, f.observationErr
	}
	if _, ok := f.intents[teamID]; !ok {
		return apikit.TeamNamespaceObservation{}, sql.ErrNoRows
	}
	clusterKey := req.ClusterKey
	if clusterKey == "" {
		clusterKey = "default"
	}
	if req.ObserverEpoch < f.observerEpochs[clusterKey] {
		return apikit.TeamNamespaceObservation{}, teamintent.ErrStaleObservation
	}
	now := time.Now().UTC()
	obs := apikit.TeamNamespaceObservation{
		ObserverID:         req.ObserverID,
		ObserverEpoch:      req.ObserverEpoch,
		ObservedRevisionID: req.ObservedRevisionID,
		NamespaceUID:       req.NamespaceUID,
		Health:             req.Health,
		Reason:             req.Reason,
		Message:            req.Message,
		Conditions:         req.Conditions,
		LastTransitionAt:   now,
		LastVerifiedAt:     now,
		UpdatedAt:          now,
	}
	item := f.intents[teamID]
	item.Observation = &obs
	f.intents[teamID] = item
	return obs, nil
}

func TestTeamIntentInternalRoutesRequireInternalToken(t *testing.T) {
	mux := newTestServer(&teamIntentFakeStore{intents: map[string]apikit.TeamNamespaceIntent{}})
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/internal/team-intents"},
		{http.MethodGet, "/internal/team-intents/team-1"},
		{http.MethodPost, "/internal/team-intents/team-1/fence"},
		{http.MethodPut, "/internal/team-intents/team-1/receipt"},
		{http.MethodPut, "/internal/team-intents/team-1/observation"},
		{http.MethodPost, "/internal/team-intents/observer-lease"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestTeamIntentInternalRoutesReturn503WhenDisabled(t *testing.T) {
	mux := newTestServer(&teamIntentFakeStore{disabled: true})
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/internal/team-intents", ""},
		{http.MethodGet, "/internal/team-intents/team-1", ""},
		{http.MethodPost, "/internal/team-intents/team-1/fence", `{"worker_id":"w1"}`},
		{http.MethodPut, "/internal/team-intents/team-1/receipt", `{"desired_revision_id":"rev-1","fence_epoch":1,"outcome":"Applied","worker_id":"w1"}`},
		{http.MethodPut, "/internal/team-intents/team-1/observation", `{"observer_id":"obs-1","observer_epoch":1,"health":"Ready"}`},
		{http.MethodPost, "/internal/team-intents/observer-lease", `{"observer_id":"obs-1"}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Authorization", "Bearer internal-token")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s status = %d, want 503 (body=%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestTeamIntentInternalLifecycleAndConflictHandling(t *testing.T) {
	store := &teamIntentFakeStore{
		intents: map[string]apikit.TeamNamespaceIntent{
			"team-1": {
				TeamID:            "team-1",
				Slug:              "alpha",
				Name:              "Alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-1",
				ActiveOwnerTeamID: "team-1",
				UpdatedAt:         time.Now().UTC(),
			},
		},
	}
	mux := newTestServer(store)

	do := func(method, path string, payload any) *httptest.ResponseRecorder {
		var body bytes.Buffer
		if payload != nil {
			if err := json.NewEncoder(&body).Encode(payload); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &body)
		req.Header.Set("Authorization", "Bearer internal-token")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// List intents
	rec := do(http.MethodGet, "/internal/team-intents", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /internal/team-intents status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var listResp apikit.TeamNamespaceIntentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil || len(listResp.Intents) != 1 {
		t.Fatalf("list response = %s, err = %v", rec.Body.String(), err)
	}

	// Get unknown team -> 404
	if rec := do(http.MethodGet, "/internal/team-intents/missing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing team status = %d, want 404", rec.Code)
	}

	// Claim fence twice: second claim invalidates first epoch
	rec1 := do(http.MethodPost, "/internal/team-intents/team-1/fence", apikit.TeamIntentFenceRequest{WorkerID: "worker-a"})
	if rec1.Code != http.StatusOK {
		t.Fatalf("POST fence 1 status = %d: %s", rec1.Code, rec1.Body.String())
	}
	var fence1 apikit.TeamIntentFenceResponse
	_ = json.Unmarshal(rec1.Body.Bytes(), &fence1)
	if fence1.FenceEpoch != 1 {
		t.Fatalf("fence1.FenceEpoch = %d, want 1", fence1.FenceEpoch)
	}

	rec2 := do(http.MethodPost, "/internal/team-intents/team-1/fence", apikit.TeamIntentFenceRequest{WorkerID: "worker-b"})
	var fence2 apikit.TeamIntentFenceResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &fence2)
	if fence2.FenceEpoch != 2 {
		t.Fatalf("fence2.FenceEpoch = %d, want 2", fence2.FenceEpoch)
	}

	// Stale fence receipt -> 409 Conflict
	staleRec := do(http.MethodPut, "/internal/team-intents/team-1/receipt", apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: "rev-1",
		FenceEpoch:        fence1.FenceEpoch,
		Outcome:           apikit.TeamIntentOutcomeApplied,
		NamespaceUID:      "ns-uid-1",
		Attempt:           1,
		WorkerID:          "worker-a",
	})
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale receipt status = %d, want 409: %s", staleRec.Code, staleRec.Body.String())
	}

	// Current fence receipt -> 200 OK
	okRec := do(http.MethodPut, "/internal/team-intents/team-1/receipt", apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: "rev-1",
		FenceEpoch:        fence2.FenceEpoch,
		Outcome:           apikit.TeamIntentOutcomeApplied,
		NamespaceUID:      "ns-uid-1",
		Attempt:           1,
		WorkerID:          "worker-b",
	})
	if okRec.Code != http.StatusOK {
		t.Fatalf("valid receipt status = %d, want 200: %s", okRec.Code, okRec.Body.String())
	}

	// Observer lease + observation epoch fencing
	lease1Rec := do(http.MethodPost, "/internal/team-intents/observer-lease", apikit.TeamIntentObserverLeaseRequest{ObserverID: "obs-1"})
	if lease1Rec.Code != http.StatusOK {
		t.Fatalf("observer lease 1 status = %d: %s", lease1Rec.Code, lease1Rec.Body.String())
	}
	var lease1 apikit.TeamIntentObserverLeaseResponse
	_ = json.Unmarshal(lease1Rec.Body.Bytes(), &lease1)

	lease2Rec := do(http.MethodPost, "/internal/team-intents/observer-lease", apikit.TeamIntentObserverLeaseRequest{ObserverID: "obs-2"})
	var lease2 apikit.TeamIntentObserverLeaseResponse
	_ = json.Unmarshal(lease2Rec.Body.Bytes(), &lease2)
	if lease2.ObserverEpoch <= lease1.ObserverEpoch {
		t.Fatalf("lease2 epoch %d should exceed lease1 epoch %d", lease2.ObserverEpoch, lease1.ObserverEpoch)
	}

	// Stale observer epoch -> 409
	staleObs := do(http.MethodPut, "/internal/team-intents/team-1/observation", apikit.TeamIntentObservationRequest{
		ObserverID:         "obs-1",
		ObserverEpoch:      lease1.ObserverEpoch,
		ObservedRevisionID: "rev-1",
		NamespaceUID:       "ns-uid-1",
		Health:             apikit.TeamIntentHealthReady,
		Reason:             "NamespaceReady",
	})
	if staleObs.Code != http.StatusConflict {
		t.Fatalf("stale observation status = %d, want 409: %s", staleObs.Code, staleObs.Body.String())
	}

	// Current observer epoch -> 200
	okObs := do(http.MethodPut, "/internal/team-intents/team-1/observation", apikit.TeamIntentObservationRequest{
		ObserverID:         "obs-2",
		ObserverEpoch:      lease2.ObserverEpoch,
		ObservedRevisionID: "rev-1",
		NamespaceUID:       "ns-uid-1",
		Health:             apikit.TeamIntentHealthReady,
		Reason:             "NamespaceReady",
	})
	if okObs.Code != http.StatusOK {
		t.Fatalf("valid observation status = %d, want 200: %s", okObs.Code, okObs.Body.String())
	}
}
