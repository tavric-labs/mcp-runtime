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

	"mcp-platform-api/internal/accessintent"
	"mcp-runtime/pkg/apikit"
)

type accessIntentFakeStore struct {
	fakeStore
	disabled       bool
	grants         map[string]apikit.GrantIntent
	grantEpochs    map[string]int64
	enrollments    map[string]apikit.EnrollmentIntent
	enrollEpochs   map[string]int64
	cleanups       map[string]apikit.AgentCleanupIntent
	cleanupEpochs  map[string]int64
	observerEpochs map[string]int64
	activateErr    error
}

func (f *accessIntentFakeStore) ListGrantIntents(context.Context) ([]apikit.GrantIntent, error) {
	if f.disabled {
		return nil, accessintent.ErrDisabled
	}
	out := make([]apikit.GrantIntent, 0, len(f.grants))
	for _, g := range f.grants {
		out = append(out, g)
	}
	return out, nil
}

func (f *accessIntentFakeStore) UpsertGrantIntent(_ context.Context, req apikit.GrantIntentUpsertRequest) (apikit.GrantIntent, error) {
	if f.disabled {
		return apikit.GrantIntent{}, accessintent.ErrDisabled
	}
	if f.grants == nil {
		f.grants = map[string]apikit.GrantIntent{}
	}
	id := req.GrantID
	if id == "" {
		id = "grant-1"
	}
	now := time.Now().UTC()
	item := apikit.GrantIntent{
		GrantID:           id,
		Namespace:         req.Namespace,
		Name:              req.Name,
		TeamID:            req.TeamID,
		ServerName:        req.ServerName,
		ServerUID:         req.ServerUID,
		Subject:           req.Subject,
		SpecJSON:          req.SpecJSON,
		DesiredRevisionID: "rev-1",
		State:             apikit.GrantIntentStateActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	item.GatewayPropagation = apikit.ComputeGrantGatewayPropagation(item.State, item.DesiredRevisionID, item.RevocationCommittedAt, item.Receipt, item.Observation)
	f.grants[id] = item
	return item, nil
}

func (f *accessIntentFakeStore) GetGrantIntent(_ context.Context, grantID string) (apikit.GrantIntent, bool, error) {
	if f.disabled {
		return apikit.GrantIntent{}, false, accessintent.ErrDisabled
	}
	item, ok := f.grants[grantID]
	return item, ok, nil
}

func (f *accessIntentFakeStore) RevokeGrantIntent(_ context.Context, grantID string, deleteGrant bool) (apikit.GrantIntent, error) {
	if f.disabled {
		return apikit.GrantIntent{}, accessintent.ErrDisabled
	}
	item, ok := f.grants[grantID]
	if !ok {
		return apikit.GrantIntent{}, sql.ErrNoRows
	}
	now := time.Now().UTC()
	item.State = apikit.GrantIntentStateRevoked
	if deleteGrant {
		item.State = apikit.GrantIntentStateDeleted
	}
	item.DesiredRevisionID = "rev-2"
	item.RevocationCommittedAt = &now
	item.UpdatedAt = now
	item.GatewayPropagation = apikit.ComputeGrantGatewayPropagation(item.State, item.DesiredRevisionID, item.RevocationCommittedAt, item.Receipt, item.Observation)
	f.grants[grantID] = item
	return item, nil
}

func (f *accessIntentFakeStore) ClaimGrantIntentFence(_ context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error) {
	if f.disabled {
		return apikit.GrantIntentFenceResponse{}, accessintent.ErrDisabled
	}
	item, ok := f.grants[grantID]
	if !ok {
		return apikit.GrantIntentFenceResponse{}, sql.ErrNoRows
	}
	if f.grantEpochs == nil {
		f.grantEpochs = map[string]int64{}
	}
	f.grantEpochs[grantID]++
	epoch := f.grantEpochs[grantID]
	item.Receipt = &apikit.GrantReceipt{FenceEpoch: epoch, FenceHolder: workerID, UpdatedAt: time.Now().UTC()}
	f.grants[grantID] = item
	return apikit.GrantIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

func (f *accessIntentFakeStore) RecordGrantIntentReceipt(_ context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error) {
	if f.disabled {
		return apikit.GrantReceipt{}, accessintent.ErrDisabled
	}
	item, ok := f.grants[grantID]
	if !ok {
		return apikit.GrantReceipt{}, sql.ErrNoRows
	}
	if req.FenceEpoch != f.grantEpochs[grantID] || req.DesiredRevisionID != item.DesiredRevisionID {
		return apikit.GrantReceipt{}, accessintent.ErrStaleReceipt
	}
	now := time.Now().UTC()
	receipt := apikit.GrantReceipt{
		FenceEpoch:        req.FenceEpoch,
		FenceHolder:       req.WorkerID,
		AppliedRevisionID: req.DesiredRevisionID,
		TargetUID:         req.TargetUID,
		TargetGeneration:  req.TargetGeneration,
		SpecDigest:        req.SpecDigest,
		Outcome:           req.Outcome,
		WorkerID:          req.WorkerID,
		AppliedAt:         &now,
		UpdatedAt:         now,
	}
	item.Receipt = &receipt
	f.grants[grantID] = item
	return receipt, nil
}

func (f *accessIntentFakeStore) RecordGrantIntentObservation(_ context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error) {
	if f.disabled {
		return apikit.GrantObservation{}, accessintent.ErrDisabled
	}
	item, ok := f.grants[grantID]
	if !ok {
		return apikit.GrantObservation{}, sql.ErrNoRows
	}
	ck := req.ClusterKey
	if ck == "" {
		ck = "default"
	}
	if req.ObserverEpoch < f.observerEpochs[ck] {
		return apikit.GrantObservation{}, accessintent.ErrStaleObservation
	}
	now := time.Now().UTC()
	obs := apikit.GrantObservation{
		ObserverID:           req.ObserverID,
		ObserverEpoch:        req.ObserverEpoch,
		ObservedRevisionID:   req.ObservedRevisionID,
		TargetUID:            req.TargetUID,
		Health:               req.Health,
		PropagationStatus:    req.PropagationStatus,
		ServingReplicas:      req.ServingReplicas,
		AcknowledgedReplicas: req.AcknowledgedReplicas,
		GatewayEvidence:      req.GatewayEvidence,
		LastTransitionAt:     now,
		LastVerifiedAt:       now,
		UpdatedAt:            now,
	}
	item.Observation = &obs
	f.grants[grantID] = item
	return obs, nil
}

func (f *accessIntentFakeStore) ListEnrollmentIntents(context.Context) ([]apikit.EnrollmentIntent, error) {
	if f.disabled {
		return nil, accessintent.ErrDisabled
	}
	out := make([]apikit.EnrollmentIntent, 0, len(f.enrollments))
	for _, e := range f.enrollments {
		out = append(out, e)
	}
	return out, nil
}

func (f *accessIntentFakeStore) CreateEnrollmentIntent(_ context.Context, req apikit.EnrollmentIntentCreateRequest) (apikit.EnrollmentIntent, bool, error) {
	if f.disabled {
		return apikit.EnrollmentIntent{}, false, accessintent.ErrDisabled
	}
	if f.enrollments == nil {
		f.enrollments = map[string]apikit.EnrollmentIntent{}
	}
	for _, existing := range f.enrollments {
		if existing.SessionUID == req.SessionUID && existing.ServerUID == req.ServerUID && existing.CSRSHA256 == req.CSRSHA256 {
			return existing, false, nil
		}
	}
	now := time.Now().UTC()
	item := apikit.EnrollmentIntent{
		OperationID:            "op-1",
		SessionUID:             req.SessionUID,
		ServerUID:              req.ServerUID,
		AgentID:                req.AgentID,
		Namespace:              req.Namespace,
		SessionName:            req.SessionName,
		CSRSHA256:              req.CSRSHA256,
		DesiredRevisionID:      "rev-e1",
		State:                  apikit.EnrollmentStatePending,
		CertificateRequestName: accessintent.DeterministicCertificateRequestName(req.SessionUID, req.ServerUID, req.CSRSHA256),
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	f.enrollments[item.OperationID] = item
	return item, true, nil
}

func (f *accessIntentFakeStore) GetEnrollmentIntent(_ context.Context, opID string) (apikit.EnrollmentIntent, bool, error) {
	if f.disabled {
		return apikit.EnrollmentIntent{}, false, accessintent.ErrDisabled
	}
	item, ok := f.enrollments[opID]
	return item, ok, nil
}

func (f *accessIntentFakeStore) ClaimEnrollmentIntentFence(_ context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error) {
	if f.disabled {
		return apikit.EnrollmentIntentFenceResponse{}, accessintent.ErrDisabled
	}
	item, ok := f.enrollments[opID]
	if !ok {
		return apikit.EnrollmentIntentFenceResponse{}, sql.ErrNoRows
	}
	if f.enrollEpochs == nil {
		f.enrollEpochs = map[string]int64{}
	}
	f.enrollEpochs[opID]++
	epoch := f.enrollEpochs[opID]
	return apikit.EnrollmentIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

func (f *accessIntentFakeStore) RecordEnrollmentIntentReceipt(_ context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error) {
	if f.disabled {
		return apikit.EnrollmentReceipt{}, accessintent.ErrDisabled
	}
	item, ok := f.enrollments[opID]
	if !ok {
		return apikit.EnrollmentReceipt{}, sql.ErrNoRows
	}
	if req.FenceEpoch != f.enrollEpochs[opID] || req.DesiredRevisionID != item.DesiredRevisionID {
		return apikit.EnrollmentReceipt{}, accessintent.ErrStaleReceipt
	}
	return apikit.EnrollmentReceipt{
		FenceEpoch:             req.FenceEpoch,
		AppliedRevisionID:      req.DesiredRevisionID,
		CertificateRequestName: item.CertificateRequestName,
		CertificateRequestUID:  req.CertificateRequestUID,
		Outcome:                req.Outcome,
		WorkerID:               req.WorkerID,
		UpdatedAt:              time.Now().UTC(),
	}, nil
}

func (f *accessIntentFakeStore) ActivateEnrollmentIntent(_ context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error) {
	if f.disabled {
		return apikit.EnrollmentIntent{}, accessintent.ErrDisabled
	}
	if f.activateErr != nil {
		return apikit.EnrollmentIntent{}, f.activateErr
	}
	item, ok := f.enrollments[opID]
	if !ok {
		return apikit.EnrollmentIntent{}, sql.ErrNoRows
	}
	if req.FenceEpoch != f.enrollEpochs[opID] || req.DesiredRevisionID != item.DesiredRevisionID {
		return apikit.EnrollmentIntent{}, accessintent.ErrStaleReceipt
	}
	now := time.Now().UTC()
	item.State = apikit.EnrollmentStateActivated
	item.ActivatedAt = &now
	f.enrollments[opID] = item
	return item, nil
}

func (f *accessIntentFakeStore) GetAgentCleanupIntent(_ context.Context, agentID string) (apikit.AgentCleanupIntent, bool, error) {
	if f.disabled {
		return apikit.AgentCleanupIntent{}, false, accessintent.ErrDisabled
	}
	item, ok := f.cleanups[agentID]
	return item, ok, nil
}

func (f *accessIntentFakeStore) ClaimAgentCleanupIntentFence(_ context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error) {
	if f.disabled {
		return apikit.AgentCleanupFenceResponse{}, accessintent.ErrDisabled
	}
	item, ok := f.cleanups[agentID]
	if !ok {
		return apikit.AgentCleanupFenceResponse{}, sql.ErrNoRows
	}
	if f.cleanupEpochs == nil {
		f.cleanupEpochs = map[string]int64{}
	}
	f.cleanupEpochs[agentID]++
	return apikit.AgentCleanupFenceResponse{FenceEpoch: f.cleanupEpochs[agentID], Intent: item}, nil
}

func (f *accessIntentFakeStore) RecordAgentCleanupIntentReceipt(_ context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error) {
	if f.disabled {
		return apikit.AgentCleanupReceipt{}, accessintent.ErrDisabled
	}
	item, ok := f.cleanups[agentID]
	if !ok {
		return apikit.AgentCleanupReceipt{}, sql.ErrNoRows
	}
	if req.FenceEpoch != f.cleanupEpochs[agentID] || req.DesiredRevisionID != item.DesiredRevisionID {
		return apikit.AgentCleanupReceipt{}, accessintent.ErrStaleReceipt
	}
	return apikit.AgentCleanupReceipt{
		FenceEpoch:        req.FenceEpoch,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		WorkerID:          req.WorkerID,
		UpdatedAt:         time.Now().UTC(),
	}, nil
}

func (f *accessIntentFakeStore) ClaimAccessIntentObserverLease(_ context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error) {
	if f.disabled {
		return apikit.AccessIntentObserverLeaseResponse{}, accessintent.ErrDisabled
	}
	if clusterKey == "" {
		clusterKey = "default"
	}
	if f.observerEpochs == nil {
		f.observerEpochs = map[string]int64{}
	}
	f.observerEpochs[clusterKey]++
	return apikit.AccessIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: f.observerEpochs[clusterKey],
	}, nil
}

func TestAccessIntentInternalRoutesRequireInternalToken(t *testing.T) {
	mux := newTestServer(&accessIntentFakeStore{})
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/internal/access-intents/grants"},
		{http.MethodPost, "/internal/access-intents/grants"},
		{http.MethodGet, "/internal/access-intents/grants/grant-1"},
		{http.MethodDelete, "/internal/access-intents/grants/grant-1"},
		{http.MethodPost, "/internal/access-intents/grants/grant-1/fence"},
		{http.MethodPut, "/internal/access-intents/grants/grant-1/receipt"},
		{http.MethodPut, "/internal/access-intents/grants/grant-1/observation"},
		{http.MethodGet, "/internal/access-intents/enrollments"},
		{http.MethodPost, "/internal/access-intents/enrollments"},
		{http.MethodGet, "/internal/access-intents/enrollments/op-1"},
		{http.MethodPost, "/internal/access-intents/enrollments/op-1/fence"},
		{http.MethodPut, "/internal/access-intents/enrollments/op-1/receipt"},
		{http.MethodPost, "/internal/access-intents/enrollments/op-1/activate"},
		{http.MethodGet, "/internal/access-intents/agent-cleanups/agt-1"},
		{http.MethodPost, "/internal/access-intents/agent-cleanups/agt-1/fence"},
		{http.MethodPut, "/internal/access-intents/agent-cleanups/agt-1/receipt"},
		{http.MethodPost, "/internal/access-intents/observer-lease"},
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

func TestAccessIntentInternalRoutesReturn503WhenDisabled(t *testing.T) {
	mux := newTestServer(&accessIntentFakeStore{disabled: true})
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/internal/access-intents/grants", ""},
		{http.MethodPost, "/internal/access-intents/grants", `{"namespace":"ns","name":"g","team_id":"t","server_name":"s","server_uid":"u"}`},
		{http.MethodGet, "/internal/access-intents/grants/grant-1", ""},
		{http.MethodDelete, "/internal/access-intents/grants/grant-1", ""},
		{http.MethodPost, "/internal/access-intents/grants/grant-1/fence", `{"worker_id":"w1"}`},
		{http.MethodPut, "/internal/access-intents/grants/grant-1/receipt", `{"desired_revision_id":"r1","fence_epoch":1,"outcome":"Applied","worker_id":"w1"}`},
		{http.MethodPut, "/internal/access-intents/grants/grant-1/observation", `{"observer_id":"o1","observer_epoch":1,"health":"Ready","propagation_status":"Verified"}`},
		{http.MethodGet, "/internal/access-intents/enrollments", ""},
		{http.MethodPost, "/internal/access-intents/enrollments", `{"session_uid":"s","server_uid":"u","namespace":"ns","session_name":"sn","csr_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`},
		{http.MethodGet, "/internal/access-intents/enrollments/op-1", ""},
		{http.MethodPost, "/internal/access-intents/enrollments/op-1/fence", `{"worker_id":"w1"}`},
		{http.MethodPut, "/internal/access-intents/enrollments/op-1/receipt", `{"desired_revision_id":"r1","fence_epoch":1,"outcome":"Applied","worker_id":"w1"}`},
		{http.MethodPost, "/internal/access-intents/enrollments/op-1/activate", `{"desired_revision_id":"r1","fence_epoch":1,"worker_id":"w1"}`},
		{http.MethodGet, "/internal/access-intents/agent-cleanups/agt-1", ""},
		{http.MethodPost, "/internal/access-intents/agent-cleanups/agt-1/fence", `{"worker_id":"w1"}`},
		{http.MethodPut, "/internal/access-intents/agent-cleanups/agt-1/receipt", `{"desired_revision_id":"r1","fence_epoch":1,"outcome":"Cleaned","worker_id":"w1"}`},
		{http.MethodPost, "/internal/access-intents/observer-lease", `{"observer_id":"o1"}`},
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

func TestAccessIntentInternalLifecycleAndConflictHandling(t *testing.T) {
	store := &accessIntentFakeStore{
		cleanups: map[string]apikit.AgentCleanupIntent{
			"agt-1": {
				AgentID:              "agt-1",
				TeamID:               "team-1",
				DesiredRevisionID:    "rev-c1",
				DeactivatedAt:        time.Now().UTC(),
				RevokedSessionsCount: 2,
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

	// Upsert grant
	rec := do(http.MethodPost, "/internal/access-intents/grants", apikit.GrantIntentUpsertRequest{
		GrantID: "grant-1", Namespace: "mcp-team-alpha", Name: "allow-read", TeamID: "team-1",
		ServerName: "docs", ServerUID: "srv-uid-1", SpecJSON: json.RawMessage(`{"mode":"enforce"}`),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST grants status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// Fence + stale fence 409
	f1Rec := do(http.MethodPost, "/internal/access-intents/grants/grant-1/fence", apikit.GrantIntentFenceRequest{WorkerID: "w1"})
	var f1 apikit.GrantIntentFenceResponse
	_ = json.Unmarshal(f1Rec.Body.Bytes(), &f1)
	f2Rec := do(http.MethodPost, "/internal/access-intents/grants/grant-1/fence", apikit.GrantIntentFenceRequest{WorkerID: "w2"})
	var f2 apikit.GrantIntentFenceResponse
	_ = json.Unmarshal(f2Rec.Body.Bytes(), &f2)

	stale := do(http.MethodPut, "/internal/access-intents/grants/grant-1/receipt", apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: f1.Intent.DesiredRevisionID, FenceEpoch: f1.FenceEpoch, Outcome: apikit.AccessIntentOutcomeApplied, WorkerID: "w1",
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale grant receipt status = %d, want 409", stale.Code)
	}
	okRec := do(http.MethodPut, "/internal/access-intents/grants/grant-1/receipt", apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: f2.Intent.DesiredRevisionID, FenceEpoch: f2.FenceEpoch, Outcome: apikit.AccessIntentOutcomeApplied, TargetUID: "uid-1", TargetGeneration: 1, SpecDigest: "sha256:aaa", WorkerID: "w2",
	})
	if okRec.Code != http.StatusOK {
		t.Fatalf("valid grant receipt status = %d, want 200: %s", okRec.Code, okRec.Body.String())
	}

	// Observer lease + stale observer epoch 409
	l1Rec := do(http.MethodPost, "/internal/access-intents/observer-lease", apikit.AccessIntentObserverLeaseRequest{ObserverID: "obs-1"})
	var l1 apikit.AccessIntentObserverLeaseResponse
	_ = json.Unmarshal(l1Rec.Body.Bytes(), &l1)
	l2Rec := do(http.MethodPost, "/internal/access-intents/observer-lease", apikit.AccessIntentObserverLeaseRequest{ObserverID: "obs-2"})
	var l2 apikit.AccessIntentObserverLeaseResponse
	_ = json.Unmarshal(l2Rec.Body.Bytes(), &l2)

	staleObs := do(http.MethodPut, "/internal/access-intents/grants/grant-1/observation", apikit.GrantIntentObservationRequest{
		ObserverID: "obs-1", ObserverEpoch: l1.ObserverEpoch, ObservedRevisionID: f2.Intent.DesiredRevisionID,
		Health: apikit.AccessIntentHealthReady, PropagationStatus: apikit.GrantPropagationVerified, ServingReplicas: 1, AcknowledgedReplicas: 1,
	})
	if staleObs.Code != http.StatusConflict {
		t.Fatalf("stale observation status = %d, want 409", staleObs.Code)
	}
	okObs := do(http.MethodPut, "/internal/access-intents/grants/grant-1/observation", apikit.GrantIntentObservationRequest{
		ObserverID: "obs-2", ObserverEpoch: l2.ObserverEpoch, ObservedRevisionID: f2.Intent.DesiredRevisionID,
		Health: apikit.AccessIntentHealthReady, PropagationStatus: apikit.GrantPropagationVerified, ServingReplicas: 1, AcknowledgedReplicas: 1,
	})
	if okObs.Code != http.StatusOK {
		t.Fatalf("valid observation status = %d, want 200: %s", okObs.Code, okObs.Body.String())
	}

	// Delete / revoke grant commits revocation synchronously
	delRec := do(http.MethodDelete, "/internal/access-intents/grants/grant-1?delete=true", nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE grant status = %d, want 200: %s", delRec.Code, delRec.Body.String())
	}

	// Enrollment create (idempotent: 201 first, 200 second) + fence + conditional activate 409 on revoked authority
	enReq := apikit.EnrollmentIntentCreateRequest{
		SessionUID: "sess-uid-1", ServerUID: "srv-uid-1", AgentID: "agt-1", Namespace: "mcp-team-alpha", SessionName: "sess-1",
		CSRSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	en1 := do(http.MethodPost, "/internal/access-intents/enrollments", enReq)
	if en1.Code != http.StatusCreated {
		t.Fatalf("POST enrollment 1 status = %d, want 201: %s", en1.Code, en1.Body.String())
	}
	en2 := do(http.MethodPost, "/internal/access-intents/enrollments", enReq)
	if en2.Code != http.StatusOK {
		t.Fatalf("POST enrollment 2 (idempotent) status = %d, want 200: %s", en2.Code, en2.Body.String())
	}
	efRec := do(http.MethodPost, "/internal/access-intents/enrollments/op-1/fence", apikit.EnrollmentIntentFenceRequest{WorkerID: "w1"})
	var ef apikit.EnrollmentIntentFenceResponse
	_ = json.Unmarshal(efRec.Body.Bytes(), &ef)

	store.activateErr = accessintent.ErrAuthorityRevoked
	revokedAct := do(http.MethodPost, "/internal/access-intents/enrollments/op-1/activate", apikit.EnrollmentIntentActivateRequest{
		DesiredRevisionID: ef.Intent.DesiredRevisionID, FenceEpoch: ef.FenceEpoch, WorkerID: "w1",
	})
	if revokedAct.Code != http.StatusConflict {
		t.Fatalf("activate with revoked authority status = %d, want 409: %s", revokedAct.Code, revokedAct.Body.String())
	}
	store.activateErr = nil
	okAct := do(http.MethodPost, "/internal/access-intents/enrollments/op-1/activate", apikit.EnrollmentIntentActivateRequest{
		DesiredRevisionID: ef.Intent.DesiredRevisionID, FenceEpoch: ef.FenceEpoch, WorkerID: "w1",
	})
	if okAct.Code != http.StatusOK {
		t.Fatalf("activate status = %d, want 200: %s", okAct.Code, okAct.Body.String())
	}

	// Agent cleanup get + fence + receipt
	cGet := do(http.MethodGet, "/internal/access-intents/agent-cleanups/agt-1", nil)
	if cGet.Code != http.StatusOK {
		t.Fatalf("GET agent cleanup status = %d, want 200", cGet.Code)
	}
	cfRec := do(http.MethodPost, "/internal/access-intents/agent-cleanups/agt-1/fence", apikit.AgentCleanupFenceRequest{WorkerID: "w1"})
	var cf apikit.AgentCleanupFenceResponse
	_ = json.Unmarshal(cfRec.Body.Bytes(), &cf)
	cReceipt := do(http.MethodPut, "/internal/access-intents/agent-cleanups/agt-1/receipt", apikit.AgentCleanupReceiptRequest{
		DesiredRevisionID: cf.Intent.DesiredRevisionID, FenceEpoch: cf.FenceEpoch, Outcome: apikit.AccessIntentOutcomeCleaned, WorkerID: "w1",
	})
	if cReceipt.Code != http.StatusOK {
		t.Fatalf("PUT agent cleanup receipt status = %d, want 200: %s", cReceipt.Code, cReceipt.Body.String())
	}
}
