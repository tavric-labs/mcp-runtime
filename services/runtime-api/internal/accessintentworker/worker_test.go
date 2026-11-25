package accessintentworker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
)

type fakePlatformClient struct {
	mu sync.Mutex

	grants           map[string]apikit.GrantIntent
	grantEpochs      map[string]int64
	grantReceipts    []apikit.GrantIntentReceiptRequest
	grantReceiptErr  error
	onPostApplyCheck func(grantID string)

	enrollments    map[string]apikit.EnrollmentIntent
	enrollEpochs   map[string]int64
	enrollReceipts []apikit.EnrollmentIntentReceiptRequest
	activateCalls  []apikit.EnrollmentIntentActivateRequest
	activateErr    error

	cleanups        map[string]apikit.AgentCleanupIntent
	cleanupEpochs   map[string]int64
	cleanupReceipts []apikit.AgentCleanupReceiptRequest
}

func (f *fakePlatformClient) ClaimGrantIntentFence(_ context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.grants[grantID]
	if !ok {
		return apikit.GrantIntentFenceResponse{}, platformapi.ErrAccessIntentNotFound
	}
	if f.grantEpochs == nil {
		f.grantEpochs = map[string]int64{}
	}
	f.grantEpochs[grantID]++
	return apikit.GrantIntentFenceResponse{FenceEpoch: f.grantEpochs[grantID], Intent: item}, nil
}

func (f *fakePlatformClient) GetGrantIntent(_ context.Context, grantID string) (apikit.GrantIntent, bool, error) {
	f.mu.Lock()
	cb := f.onPostApplyCheck
	f.mu.Unlock()
	if cb != nil {
		cb(grantID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.grants[grantID]
	return item, ok, nil
}

func (f *fakePlatformClient) RecordGrantIntentReceipt(_ context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grantReceiptErr != nil {
		return apikit.GrantReceipt{}, f.grantReceiptErr
	}
	f.grantReceipts = append(f.grantReceipts, req)
	return apikit.GrantReceipt{FenceEpoch: req.FenceEpoch, AppliedRevisionID: req.DesiredRevisionID, Outcome: req.Outcome}, nil
}

func (f *fakePlatformClient) ClaimEnrollmentIntentFence(_ context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.enrollments[opID]
	if !ok {
		return apikit.EnrollmentIntentFenceResponse{}, platformapi.ErrAccessIntentNotFound
	}
	if f.enrollEpochs == nil {
		f.enrollEpochs = map[string]int64{}
	}
	f.enrollEpochs[opID]++
	return apikit.EnrollmentIntentFenceResponse{FenceEpoch: f.enrollEpochs[opID], Intent: item}, nil
}

func (f *fakePlatformClient) RecordEnrollmentIntentReceipt(_ context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrollReceipts = append(f.enrollReceipts, req)
	return apikit.EnrollmentReceipt{FenceEpoch: req.FenceEpoch, Outcome: req.Outcome}, nil
}

func (f *fakePlatformClient) ActivateEnrollmentIntent(_ context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activateCalls = append(f.activateCalls, req)
	if f.activateErr != nil {
		return apikit.EnrollmentIntent{}, f.activateErr
	}
	item := f.enrollments[opID]
	item.State = apikit.EnrollmentStateActivated
	f.enrollments[opID] = item
	return item, nil
}

func (f *fakePlatformClient) ClaimAgentCleanupIntentFence(_ context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.cleanups[agentID]
	if !ok {
		return apikit.AgentCleanupFenceResponse{}, platformapi.ErrAccessIntentNotFound
	}
	if f.cleanupEpochs == nil {
		f.cleanupEpochs = map[string]int64{}
	}
	f.cleanupEpochs[agentID]++
	return apikit.AgentCleanupFenceResponse{FenceEpoch: f.cleanupEpochs[agentID], Intent: item}, nil
}

func (f *fakePlatformClient) RecordAgentCleanupIntentReceipt(_ context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanupReceipts = append(f.cleanupReceipts, req)
	return apikit.AgentCleanupReceipt{FenceEpoch: req.FenceEpoch, Outcome: req.Outcome}, nil
}

type fakeGrantProjector struct {
	mu           sync.Mutex
	applied      []apikit.GrantIntent
	deletedCalls []struct{ namespace, name, uid, grantID string }
	applyResult  ProjectedGrant
	applyErr     error
	deleteErr    error
}

func (p *fakeGrantProjector) ApplyGrant(_ context.Context, intent apikit.GrantIntent) (ProjectedGrant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.applyErr != nil {
		return ProjectedGrant{}, p.applyErr
	}
	p.applied = append(p.applied, intent)
	res := p.applyResult
	if res.UID == "" {
		res.UID = "cr-uid-1"
		res.Generation = 1
		res.SpecDigest = SpecDigest(intent.SpecJSON)
	}
	return res, nil
}

func (p *fakeGrantProjector) DeleteGrantByUID(_ context.Context, namespace, name, expectedUID, expectedGrantID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deleteErr != nil {
		return false, p.deleteErr
	}
	p.deletedCalls = append(p.deletedCalls, struct{ namespace, name, uid, grantID string }{namespace, name, expectedUID, expectedGrantID})
	return true, nil
}

type fakeCRManager struct {
	mu          sync.Mutex
	existing    map[string]*CertificateRequestState
	createCalls int
	createState *CertificateRequestState
}

func (m *fakeCRManager) GetCertificateRequest(_ context.Context, namespace, name string) (*CertificateRequestState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.existing[namespace+"/"+name]
	return st, ok, nil
}

func (m *fakeCRManager) CreateCertificateRequest(_ context.Context, spec CertificateRequestSpec) (*CertificateRequestState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.existing == nil {
		m.existing = map[string]*CertificateRequestState{}
	}
	st := m.createState
	if st == nil {
		st = &CertificateRequestState{Name: spec.Name, UID: "cr-uid-new", Pending: true}
	}
	m.existing[spec.Namespace+"/"+spec.Name] = st
	return st, nil
}

func TestGrantWorkerRevokeDuringApplyDeletesCRAndNeverRecordsApplied(t *testing.T) {
	client := &fakePlatformClient{
		grants: map[string]apikit.GrantIntent{
			"grant-1": {
				GrantID:           "grant-1",
				Namespace:         "mcp-team-alpha",
				Name:              "allow-docs",
				TeamID:            "team-1",
				ServerName:        "docs",
				ServerUID:         "srv-uid-1",
				SpecJSON:          json.RawMessage(`{"mode":"enforce"}`),
				DesiredRevisionID: "rev-1",
				State:             apikit.GrantIntentStateActive,
			},
		},
	}
	// Simulate revocation committing in Platform API while worker is applying CR.
	client.onPostApplyCheck = func(grantID string) {
		client.mu.Lock()
		defer client.mu.Unlock()
		g := client.grants[grantID]
		g.State = apikit.GrantIntentStateRevoked
		g.DesiredRevisionID = "rev-2"
		client.grants[grantID] = g
	}
	projector := &fakeGrantProjector{
		applyResult: ProjectedGrant{UID: "k8s-grant-uid-9", Generation: 3, SpecDigest: "sha256:123"},
	}

	worker := &GrantWorker{Platform: client, Projector: projector, WorkerID: "worker-1"}
	job := &river.Job[ReconcileGrantJob]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   ReconcileGrantJob{GrantID: "grant-1", RevisionID: "rev-1"},
	}
	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatalf("Work returned error: %v", err)
	}
	if len(projector.deletedCalls) != 1 {
		t.Fatalf("expected immediate compensating delete, got %d calls", len(projector.deletedCalls))
	}
	if projector.deletedCalls[0].uid != "k8s-grant-uid-9" || projector.deletedCalls[0].grantID != "grant-1" {
		t.Fatalf("compensating delete did not pass UID/grantID precondition: %+v", projector.deletedCalls[0])
	}
	for _, rec := range client.grantReceipts {
		if rec.Outcome == apikit.AccessIntentOutcomeApplied {
			t.Fatalf("worker must NEVER record Applied after mid-apply revocation: %+v", rec)
		}
	}
}

func TestGrantWorkerHandlesStaleReceiptFenceGracefully(t *testing.T) {
	client := &fakePlatformClient{
		grants: map[string]apikit.GrantIntent{
			"grant-1": {
				GrantID:           "grant-1",
				Namespace:         "mcp-team-alpha",
				Name:              "allow-docs",
				ServerName:        "docs",
				ServerUID:         "srv-uid-1",
				SpecJSON:          json.RawMessage(`{"mode":"enforce"}`),
				DesiredRevisionID: "rev-1",
				State:             apikit.GrantIntentStateActive,
			},
		},
		grantReceiptErr: platformapi.ErrAccessIntentConflict,
	}
	projector := &fakeGrantProjector{}
	worker := &GrantWorker{Platform: client, Projector: projector, WorkerID: "worker-stale"}
	job := &river.Job[ReconcileGrantJob]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   ReconcileGrantJob{GrantID: "grant-1", RevisionID: "rev-1"},
	}
	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatalf("stale receipt conflict should complete cleanly without retry loop, got: %v", err)
	}
}

func TestGrantWorkerDeletesCRWithUIDPreconditionWhenRevoked(t *testing.T) {
	client := &fakePlatformClient{
		grants: map[string]apikit.GrantIntent{
			"grant-1": {
				GrantID:           "grant-1",
				Namespace:         "mcp-team-alpha",
				Name:              "allow-docs",
				ServerName:        "docs",
				ServerUID:         "srv-uid-1",
				DesiredRevisionID: "rev-2",
				State:             apikit.GrantIntentStateDeleted,
				Receipt:           &apikit.GrantReceipt{TargetUID: "k8s-uid-old"},
			},
		},
	}
	projector := &fakeGrantProjector{}
	worker := &GrantWorker{Platform: client, Projector: projector, WorkerID: "worker-1"}
	job := &river.Job[ReconcileGrantJob]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   ReconcileGrantJob{GrantID: "grant-1", RevisionID: "rev-2"},
	}
	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatalf("Work err = %v", err)
	}
	if len(projector.deletedCalls) != 1 || projector.deletedCalls[0].uid != "k8s-uid-old" {
		t.Fatalf("expected UID-preconditioned delete, got %+v", projector.deletedCalls)
	}
	if len(client.grantReceipts) != 1 || client.grantReceipts[0].Outcome != apikit.AccessIntentOutcomeCleaned {
		t.Fatalf("expected Cleaned receipt, got %+v", client.grantReceipts)
	}
}

func TestEnrollmentWorkerCrashResumeDoesNotCreateDuplicateCR(t *testing.T) {
	client := &fakePlatformClient{
		enrollments: map[string]apikit.EnrollmentIntent{
			"op-1": {
				OperationID:            "op-1",
				SessionUID:             "sess-uid-1",
				ServerUID:              "srv-uid-1",
				AgentID:                "agt-1",
				Namespace:              "mcp-team-alpha",
				SessionName:            "sess-1",
				CSRSHA256:              "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				DesiredRevisionID:      "rev-e1",
				State:                  apikit.EnrollmentStatePending,
				CertificateRequestName: "enroll-deterministic-1",
			},
		},
	}
	// Simulate prior worker crash AFTER creating the deterministic CR, BEFORE recording receipt.
	crMgr := &fakeCRManager{
		existing: map[string]*CertificateRequestState{
			"mcp-team-alpha/enroll-deterministic-1": {
				Name:   "enroll-deterministic-1",
				UID:    "cr-uid-existing",
				Issued: true,
			},
		},
	}

	worker := &EnrollmentWorker{Platform: client, CRManager: crMgr, WorkerID: "worker-2"}
	job := &river.Job[ReconcileEnrollmentJob]{
		JobRow: &rivertype.JobRow{Attempt: 2},
		Args:   ReconcileEnrollmentJob{OperationID: "op-1", RevisionID: "rev-e1"},
	}
	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatalf("Work err = %v", err)
	}
	if crMgr.createCalls != 0 {
		t.Fatalf("expected 0 CreateCertificateRequest calls on crash resume, got %d", crMgr.createCalls)
	}
	if len(client.activateCalls) != 1 || client.activateCalls[0].CertificateRequestUID != "cr-uid-existing" {
		t.Fatalf("expected conditional activation for resumed CR, got %+v", client.activateCalls)
	}
}

func TestEnrollmentWorkerCancelsWhenAgentDeactivatedOrSessionRevokedDuringEnrollment(t *testing.T) {
	client := &fakePlatformClient{
		enrollments: map[string]apikit.EnrollmentIntent{
			"op-1": {
				OperationID:            "op-1",
				SessionUID:             "sess-uid-1",
				ServerUID:              "srv-uid-1",
				AgentID:                "agt-1",
				Namespace:              "mcp-team-alpha",
				SessionName:            "sess-1",
				CSRSHA256:              "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				DesiredRevisionID:      "rev-e1",
				State:                  apikit.EnrollmentStateSubmitted,
				CertificateRequestName: "enroll-deterministic-1",
			},
		},
		activateErr: platformapi.ErrAccessIntentConflict,
	}
	crMgr := &fakeCRManager{
		existing: map[string]*CertificateRequestState{
			"mcp-team-alpha/enroll-deterministic-1": {
				Name:   "enroll-deterministic-1",
				UID:    "cr-uid-issued",
				Issued: true,
			},
		},
	}

	worker := &EnrollmentWorker{Platform: client, CRManager: crMgr, WorkerID: "worker-1"}
	job := &river.Job[ReconcileEnrollmentJob]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   ReconcileEnrollmentJob{OperationID: "op-1", RevisionID: "rev-e1"},
	}
	err := worker.Work(t.Context(), job)
	var cancelErr *rivertype.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Fatalf("expected river.JobCancelError when conditional activation rejected, got %v", err)
	}
	if client.enrollments["op-1"].State == apikit.EnrollmentStateActivated {
		t.Fatal("enrollment must never be activated after authority revocation")
	}
}

func TestAgentCleanupWorkerReconcilesIdempotentlyWithoutMutatingSessionAuthority(t *testing.T) {
	now := time.Now().UTC()
	client := &fakePlatformClient{
		cleanups: map[string]apikit.AgentCleanupIntent{
			"agt-1": {
				AgentID:              "agt-1",
				TeamID:               "team-1",
				DesiredRevisionID:    "rev-c1",
				DeactivatedAt:        now,
				RevokedSessionsCount: 4,
			},
		},
	}
	called := 0
	worker := &AgentCleanupWorker{
		Platform: client,
		WorkerID: "worker-1",
		Executor: func(_ context.Context, intent apikit.AgentCleanupIntent) error {
			called++
			if intent.AgentID != "agt-1" || intent.RevokedSessionsCount != 4 {
				return errors.New("unexpected cleanup intent")
			}
			return nil
		},
	}
	job := &river.Job[ReconcileAgentCleanupJob]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   ReconcileAgentCleanupJob{AgentID: "agt-1", RevisionID: "rev-c1"},
	}
	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatalf("Work err = %v", err)
	}
	if called != 1 {
		t.Fatalf("executor called %d times, want 1", called)
	}
	if len(client.cleanupReceipts) != 1 || client.cleanupReceipts[0].Outcome != apikit.AccessIntentOutcomeCleaned {
		t.Fatalf("expected Cleaned receipt, got %+v", client.cleanupReceipts)
	}
}
