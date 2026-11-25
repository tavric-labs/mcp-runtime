package platforminternal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-platform-api/internal/platformstore"
	"mcp-platform-api/internal/workloadintent"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/passport"
)

type stubWorkloadIntentStore struct {
	stubPlatformStore
	disabled       bool
	server         apikit.ServerIntent
	deployment     apikit.DeploymentIntent
	publication    apikit.ImagePublicationIntent
	restart        apikit.RestartOperationIntent
	receiptErr     error
	observationErr error
	beginPubErr    error
	leaseEpoch     int64
}

type stubPlatformStore struct{}

func (stubPlatformStore) PrincipalForUserID(context.Context, string) (passport.Principal, error) {
	return passport.Principal{}, nil
}
func (stubPlatformStore) AuthenticateUserAPIKey(context.Context, string) (passport.Principal, bool, error) {
	return passport.Principal{}, false, nil
}
func (stubPlatformStore) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (stubPlatformStore) ResolveTeamIDs(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (stubPlatformStore) WriteAudit(context.Context, platformstore.AuditEvent) {}
func (stubPlatformStore) CreateTeam(context.Context, string, string, string) (platformstore.Team, error) {
	return platformstore.Team{}, nil
}
func (stubPlatformStore) ListTeams(context.Context) ([]platformstore.Team, error) { return nil, nil }
func (stubPlatformStore) GetTeamBySlug(context.Context, string) (platformstore.Team, bool, error) {
	return platformstore.Team{}, false, nil
}
func (stubPlatformStore) CreateAgent(context.Context, string, string, string) (platformstore.Agent, error) {
	return platformstore.Agent{}, nil
}
func (stubPlatformStore) ListAgents(context.Context, platformstore.AgentListFilter) (platformstore.AgentPage, error) {
	return platformstore.AgentPage{}, nil
}
func (stubPlatformStore) GetAgent(context.Context, string) (platformstore.Agent, bool, error) {
	return platformstore.Agent{}, false, nil
}
func (stubPlatformStore) RenameAgent(context.Context, string, string) (platformstore.Agent, error) {
	return platformstore.Agent{}, nil
}
func (stubPlatformStore) SetAgentStatus(context.Context, string, string, string) (platformstore.Agent, error) {
	return platformstore.Agent{}, nil
}
func (stubPlatformStore) DeleteTeamBySlug(context.Context, string) error { return nil }
func (stubPlatformStore) ListNamespaces(context.Context) ([]map[string]any, error) {
	return nil, nil
}
func (stubPlatformStore) GetNamespace(context.Context, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (stubPlatformStore) ListTeamMemberships(context.Context, string) ([]platformstore.TeamMembership, error) {
	return nil, nil
}
func (stubPlatformStore) UpsertTeamMembership(context.Context, string, string, string) (platformstore.TeamMembership, error) {
	return platformstore.TeamMembership{}, nil
}
func (stubPlatformStore) DeleteTeamMembership(context.Context, string, string) error { return nil }
func (stubPlatformStore) CreateTeamUser(context.Context, string, string, string, string) (platformstore.User, platformstore.TeamMembership, error) {
	return platformstore.User{}, platformstore.TeamMembership{}, nil
}
func (stubPlatformStore) CreatePasswordUser(context.Context, string, string, string) (platformstore.User, error) {
	return platformstore.User{}, nil
}
func (stubPlatformStore) OperationsSnapshot(context.Context, platformstore.OperationsFilter) (platformstore.OperationsSnapshot, error) {
	return platformstore.OperationsSnapshot{}, nil
}

func (s *stubWorkloadIntentStore) ClaimWorkloadObserverLease(_ context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error) {
	if s.disabled {
		return apikit.WorkloadObserverLeaseResponse{}, workloadintent.ErrDisabled
	}
	s.leaseEpoch++
	if clusterKey == "" {
		clusterKey = "default"
	}
	return apikit.WorkloadObserverLeaseResponse{ClusterKey: clusterKey, ObserverID: observerID, ObserverEpoch: s.leaseEpoch}, nil
}
func (s *stubWorkloadIntentStore) ListServerIntents(context.Context) ([]apikit.ServerIntent, error) {
	if s.disabled {
		return nil, workloadintent.ErrDisabled
	}
	return []apikit.ServerIntent{s.server}, nil
}
func (s *stubWorkloadIntentStore) GetServerIntent(_ context.Context, id string) (apikit.ServerIntent, bool, error) {
	if s.disabled {
		return apikit.ServerIntent{}, false, workloadintent.ErrDisabled
	}
	if id != s.server.ServerID {
		return apikit.ServerIntent{}, false, nil
	}
	return s.server, true, nil
}
func (s *stubWorkloadIntentStore) UpsertServerIntent(_ context.Context, req apikit.ServerIntentUpsertRequest) (apikit.ServerIntent, error) {
	if s.disabled {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	s.server = apikit.ServerIntent{ServerID: req.ServerID, Namespace: req.Namespace, Name: req.Name, DesiredRevisionID: "rev-1", Lifecycle: apikit.ServerLifecycleActive}
	return s.server, nil
}
func (s *stubWorkloadIntentStore) RetireServerIntent(_ context.Context, id string, req apikit.ServerIntentRetireRequest) (apikit.ServerIntent, error) {
	if s.disabled {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	if id != s.server.ServerID {
		return apikit.ServerIntent{}, sql.ErrNoRows
	}
	s.server.Lifecycle = apikit.ServerLifecycleRetiring
	s.server.TargetCRUID = req.TargetCRUID
	return s.server, nil
}
func (s *stubWorkloadIntentStore) ClaimServerIntentFence(_ context.Context, id, _ string) (apikit.ServerIntentFenceResponse, error) {
	if s.disabled {
		return apikit.ServerIntentFenceResponse{}, workloadintent.ErrDisabled
	}
	if id != s.server.ServerID {
		return apikit.ServerIntentFenceResponse{}, sql.ErrNoRows
	}
	return apikit.ServerIntentFenceResponse{FenceEpoch: 1, Intent: s.server}, nil
}
func (s *stubWorkloadIntentStore) RecordServerRetirementCheckpoint(_ context.Context, _ string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error) {
	if s.disabled {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	s.server.RetirementCheckpoints = req.Checkpoints
	return s.server, nil
}
func (s *stubWorkloadIntentStore) RecordServerIntentReceipt(_ context.Context, _ string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if s.disabled {
		return apikit.WorkloadExecutionReceipt{}, workloadintent.ErrDisabled
	}
	if s.receiptErr != nil {
		return apikit.WorkloadExecutionReceipt{}, s.receiptErr
	}
	return apikit.WorkloadExecutionReceipt{FenceEpoch: req.FenceEpoch, AppliedRevisionID: req.DesiredRevisionID, Outcome: req.Outcome}, nil
}
func (s *stubWorkloadIntentStore) RecordServerIntentObservation(_ context.Context, _ string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if s.disabled {
		return apikit.WorkloadStatusObservation{}, workloadintent.ErrDisabled
	}
	if s.observationErr != nil {
		return apikit.WorkloadStatusObservation{}, s.observationErr
	}
	return apikit.WorkloadStatusObservation{ObserverEpoch: req.ObserverEpoch, Health: req.Health}, nil
}

func (s *stubWorkloadIntentStore) ListDeploymentIntents(context.Context) ([]apikit.DeploymentIntent, error) {
	return []apikit.DeploymentIntent{s.deployment}, nil
}
func (s *stubWorkloadIntentStore) GetDeploymentIntent(_ context.Context, id string) (apikit.DeploymentIntent, bool, error) {
	return s.deployment, id == s.deployment.DeploymentID, nil
}
func (s *stubWorkloadIntentStore) UpsertDeploymentIntent(_ context.Context, req apikit.DeploymentIntentUpsertRequest) (apikit.DeploymentIntent, error) {
	s.deployment = apikit.DeploymentIntent{DeploymentID: req.DeploymentID, Namespace: req.Namespace, Name: req.Name, Image: req.Image, DesiredRevisionID: "rev-dep-1"}
	return s.deployment, nil
}
func (s *stubWorkloadIntentStore) DeleteDeploymentIntent(_ context.Context, _ string, _ apikit.DeploymentIntentDeleteRequest) (apikit.DeploymentIntent, error) {
	s.deployment.Deleted = true
	return s.deployment, nil
}
func (s *stubWorkloadIntentStore) ClaimDeploymentIntentFence(_ context.Context, _ string, _ string) (apikit.DeploymentIntentFenceResponse, error) {
	return apikit.DeploymentIntentFenceResponse{FenceEpoch: 1, Intent: s.deployment}, nil
}
func (s *stubWorkloadIntentStore) RecordDeploymentIntentReceipt(_ context.Context, _ string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	return apikit.WorkloadExecutionReceipt{FenceEpoch: req.FenceEpoch, Outcome: req.Outcome}, nil
}
func (s *stubWorkloadIntentStore) RecordDeploymentIntentObservation(_ context.Context, _ string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	return apikit.WorkloadStatusObservation{ObserverEpoch: req.ObserverEpoch, Health: req.Health}, nil
}

func (s *stubWorkloadIntentStore) ListImagePublicationIntents(context.Context) ([]apikit.ImagePublicationIntent, error) {
	return []apikit.ImagePublicationIntent{s.publication}, nil
}
func (s *stubWorkloadIntentStore) GetImagePublicationIntent(_ context.Context, id string) (apikit.ImagePublicationIntent, bool, error) {
	return s.publication, id == s.publication.PublicationID, nil
}
func (s *stubWorkloadIntentStore) BeginImagePublication(_ context.Context, req apikit.ImagePublicationBeginRequest) (apikit.ImagePublicationIntent, error) {
	if s.beginPubErr != nil {
		return apikit.ImagePublicationIntent{}, s.beginPubErr
	}
	s.publication = apikit.ImagePublicationIntent{PublicationID: req.PublicationID, Namespace: req.Namespace, Registry: req.Registry, Repository: req.Repository, Tag: req.Tag, UploadState: apikit.ImageUploadStateUploading, Status: apikit.ImagePublicationStatusUploading}
	return s.publication, nil
}
func (s *stubWorkloadIntentStore) FinalizeImagePublication(_ context.Context, _ string, req apikit.ImagePublicationFinalizeRequest) (apikit.ImagePublicationIntent, error) {
	s.publication.UploadState = apikit.ImageUploadStateFinalized
	s.publication.Status = apikit.ImagePublicationStatusQueued
	s.publication.ArtifactRef = req.ArtifactRef
	return s.publication, nil
}
func (s *stubWorkloadIntentStore) AbortImagePublication(_ context.Context, _ string, _ apikit.ImagePublicationAbortRequest) (apikit.ImagePublicationIntent, error) {
	s.publication.UploadState = apikit.ImageUploadStateAborted
	s.publication.Status = apikit.ImagePublicationStatusAborted
	return s.publication, nil
}
func (s *stubWorkloadIntentStore) ClaimImagePublicationFence(_ context.Context, _ string, _ string) (apikit.ImagePublicationFenceResponse, error) {
	return apikit.ImagePublicationFenceResponse{FenceEpoch: 1, Intent: s.publication}, nil
}
func (s *stubWorkloadIntentStore) RecordImagePublicationReceipt(_ context.Context, _ string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	return apikit.WorkloadExecutionReceipt{FenceEpoch: req.FenceEpoch, Outcome: req.Outcome}, nil
}

func (s *stubWorkloadIntentStore) ListRestartOperationIntents(context.Context) ([]apikit.RestartOperationIntent, error) {
	return []apikit.RestartOperationIntent{s.restart}, nil
}
func (s *stubWorkloadIntentStore) GetRestartOperationIntent(_ context.Context, id string) (apikit.RestartOperationIntent, bool, error) {
	return s.restart, id == s.restart.OperationID, nil
}
func (s *stubWorkloadIntentStore) CreateRestartOperation(_ context.Context, req apikit.RestartOperationCreateRequest) (apikit.RestartOperationIntent, error) {
	s.restart = apikit.RestartOperationIntent{OperationID: req.OperationID, RequestedBy: req.RequestedBy, TargetComponents: req.TargetComponents, RestartedAt: "2026-11-25T13:00:00Z", Status: apikit.RestartStatusQueued}
	return s.restart, nil
}
func (s *stubWorkloadIntentStore) ClaimRestartOperationFence(_ context.Context, _ string, _ string) (apikit.RestartOperationFenceResponse, error) {
	return apikit.RestartOperationFenceResponse{FenceEpoch: 1, Intent: s.restart}, nil
}
func (s *stubWorkloadIntentStore) RecordRestartComponentCheckpoint(_ context.Context, _ string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error) {
	if s.restart.ComponentCheckpoints == nil {
		s.restart.ComponentCheckpoints = map[string]apikit.ComponentRestartCheckpoint{}
	}
	s.restart.ComponentCheckpoints[req.Checkpoint.Component] = req.Checkpoint
	return s.restart, nil
}
func (s *stubWorkloadIntentStore) RecordRestartOperationReceipt(_ context.Context, _ string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	return apikit.WorkloadExecutionReceipt{FenceEpoch: req.FenceEpoch, Outcome: req.Outcome}, nil
}

func doInternalReq(t *testing.T, mux http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestWorkloadIntentsAuthDisabledAndConflictMapping(t *testing.T) {
	store := &stubWorkloadIntentStore{
		server: apikit.ServerIntent{ServerID: "srv-1", Namespace: "mcp-team-alpha", Name: "billing", DesiredRevisionID: "rev-1"},
	}
	mux := http.NewServeMux()
	Handler{Store: store, Token: "secret-internal"}.Register(mux)

	// Missing auth -> 401
	rr := doInternalReq(t, mux, http.MethodGet, "/internal/workload-intents/servers", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rr.Code)
	}

	// Disabled journal -> 503
	store.disabled = true
	rr = doInternalReq(t, mux, http.MethodGet, "/internal/workload-intents/servers", "secret-internal", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d, want 503", rr.Code)
	}
	store.disabled = false

	// Stale receipt -> 409
	store.receiptErr = workloadintent.ErrStaleReceipt
	rr = doInternalReq(t, mux, http.MethodPut, "/internal/workload-intents/servers/srv-1/receipt", "secret-internal", apikit.ServerIntentReceiptRequest{
		DesiredRevisionID: "rev-1", FenceEpoch: 1, Outcome: apikit.WorkloadIntentOutcomeApplied, WorkerID: "w-1",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale receipt status = %d, want 409", rr.Code)
	}
	store.receiptErr = nil

	// Stale observation -> 409
	store.observationErr = workloadintent.ErrStaleObservation
	rr = doInternalReq(t, mux, http.MethodPut, "/internal/workload-intents/servers/srv-1/observation", "secret-internal", apikit.ServerIntentObservationRequest{
		ObserverID: "obs-1", ObserverEpoch: 1, Health: apikit.WorkloadIntentHealthReady,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale observation status = %d, want 409", rr.Code)
	}
	store.observationErr = nil

	// Mutable-tag overlap conflict -> 409
	store.beginPubErr = workloadintent.ErrTagOverlapConflict
	rr = doInternalReq(t, mux, http.MethodPost, "/internal/workload-intents/publications", "secret-internal", apikit.ImagePublicationBeginRequest{
		PublicationID: "pub-1", Namespace: "mcp-team-alpha", Registry: "registry.local", Repository: "alpha/app", Tag: "latest",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("tag overlap status = %d, want 409", rr.Code)
	}
	store.beginPubErr = nil

	// Happy path observer lease + restart creation
	rr = doInternalReq(t, mux, http.MethodPost, "/internal/workload-intents/observer-lease", "secret-internal", apikit.WorkloadObserverLeaseRequest{
		ObserverID: "obs-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("observer lease status = %d, want 200", rr.Code)
	}
	rr = doInternalReq(t, mux, http.MethodPost, "/internal/workload-intents/restarts", "secret-internal", apikit.RestartOperationCreateRequest{
		OperationID: "op-1", RequestedBy: "admin", TargetComponents: []string{"gateway"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create restart status = %d, want 201", rr.Code)
	}
}
