package workloadintentworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
)

type fakePlatformClient struct {
	servers      map[string]apikit.ServerIntent
	deployments  map[string]apikit.DeploymentIntent
	publications map[string]apikit.ImagePublicationIntent
	restarts     map[string]apikit.RestartOperationIntent

	serverFenceEpoch      int64
	deploymentFenceEpoch  int64
	publicationFenceEpoch int64
	restartFenceEpoch     int64

	serverReceipts      []apikit.ServerIntentReceiptRequest
	deploymentReceipts  []apikit.DeploymentIntentReceiptRequest
	publicationReceipts []apikit.ImagePublicationReceiptRequest
	restartReceipts     []apikit.RestartOperationReceiptRequest
	serverCheckpoints   []apikit.ServerRetirementCheckpointRequest
	restartCheckpoints  []apikit.RestartComponentCheckpointRequest
}

func newFakePlatformClient() *fakePlatformClient {
	return &fakePlatformClient{
		servers:      map[string]apikit.ServerIntent{},
		deployments:  map[string]apikit.DeploymentIntent{},
		publications: map[string]apikit.ImagePublicationIntent{},
		restarts:     map[string]apikit.RestartOperationIntent{},
	}
}

func (f *fakePlatformClient) ClaimServerIntentFence(_ context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error) {
	item, ok := f.servers[serverID]
	if !ok {
		return apikit.ServerIntentFenceResponse{}, platformapi.ErrWorkloadIntentNotFound
	}
	f.serverFenceEpoch++
	return apikit.ServerIntentFenceResponse{FenceEpoch: f.serverFenceEpoch, Intent: item}, nil
}

func (f *fakePlatformClient) RecordServerRetirementCheckpoint(_ context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error) {
	item := f.servers[serverID]
	item.RetirementCheckpoints.RetiringMarked = item.RetirementCheckpoints.RetiringMarked || req.Checkpoints.RetiringMarked
	item.RetirementCheckpoints.SessionsFenced = item.RetirementCheckpoints.SessionsFenced || req.Checkpoints.SessionsFenced
	item.RetirementCheckpoints.GrantsRetired = item.RetirementCheckpoints.GrantsRetired || req.Checkpoints.GrantsRetired
	item.RetirementCheckpoints.CredentialsCleaned = item.RetirementCheckpoints.CredentialsCleaned || req.Checkpoints.CredentialsCleaned
	item.RetirementCheckpoints.CRDeleted = item.RetirementCheckpoints.CRDeleted || req.Checkpoints.CRDeleted
	if item.TargetCRUID == "" {
		item.TargetCRUID = req.TargetCRUID
	}
	if item.RetirementCheckpoints.RetiringMarked && item.RetirementCheckpoints.SessionsFenced &&
		item.RetirementCheckpoints.GrantsRetired && item.RetirementCheckpoints.CredentialsCleaned && item.RetirementCheckpoints.CRDeleted {
		item.Lifecycle = apikit.ServerLifecycleRetired
	}
	f.servers[serverID] = item
	f.serverCheckpoints = append(f.serverCheckpoints, req)
	return item, nil
}

func (f *fakePlatformClient) RecordServerIntentReceipt(_ context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	f.serverReceipts = append(f.serverReceipts, req)
	r := apikit.WorkloadExecutionReceipt{
		FenceEpoch:        req.FenceEpoch,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		TargetUID:         req.TargetUID,
		TargetGeneration:  req.TargetGeneration,
		SpecDigest:        req.SpecDigest,
		WorkerID:          req.WorkerID,
		LastErrorClass:    req.LastErrorClass,
		LastError:         req.LastError,
	}
	item := f.servers[serverID]
	item.Receipt = &r
	f.servers[serverID] = item
	return r, nil
}

func (f *fakePlatformClient) GetImagePublicationIntent(_ context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error) {
	item, ok := f.publications[publicationID]
	return item, ok, nil
}

func (f *fakePlatformClient) ClaimDeploymentIntentFence(_ context.Context, deploymentID, _ string) (apikit.DeploymentIntentFenceResponse, error) {
	item, ok := f.deployments[deploymentID]
	if !ok {
		return apikit.DeploymentIntentFenceResponse{}, platformapi.ErrWorkloadIntentNotFound
	}
	f.deploymentFenceEpoch++
	return apikit.DeploymentIntentFenceResponse{FenceEpoch: f.deploymentFenceEpoch, Intent: item}, nil
}

func (f *fakePlatformClient) RecordDeploymentIntentReceipt(_ context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	f.deploymentReceipts = append(f.deploymentReceipts, req)
	r := apikit.WorkloadExecutionReceipt{
		FenceEpoch:        req.FenceEpoch,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		TargetUID:         req.TargetUID,
		TargetGeneration:  req.TargetGeneration,
		SpecDigest:        req.SpecDigest,
		WorkerID:          req.WorkerID,
	}
	item := f.deployments[deploymentID]
	item.Receipt = &r
	f.deployments[deploymentID] = item
	return r, nil
}

func (f *fakePlatformClient) ClaimImagePublicationFence(_ context.Context, publicationID, _ string) (apikit.ImagePublicationFenceResponse, error) {
	item, ok := f.publications[publicationID]
	if !ok {
		return apikit.ImagePublicationFenceResponse{}, platformapi.ErrWorkloadIntentNotFound
	}
	f.publicationFenceEpoch++
	return apikit.ImagePublicationFenceResponse{FenceEpoch: f.publicationFenceEpoch, Intent: item}, nil
}

func (f *fakePlatformClient) RecordImagePublicationReceipt(_ context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	f.publicationReceipts = append(f.publicationReceipts, req)
	item := f.publications[publicationID]
	if req.HelperName != "" {
		item.HelperName = req.HelperName
	}
	if req.VerifiedDigest != "" {
		item.VerifiedDigest = req.VerifiedDigest
	}
	if req.Status != "" {
		item.Status = req.Status
	}
	r := apikit.WorkloadExecutionReceipt{
		FenceEpoch:        req.FenceEpoch,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		SpecDigest:        req.VerifiedDigest,
		WorkerID:          req.WorkerID,
		LastErrorClass:    req.LastErrorClass,
		LastError:         req.LastError,
	}
	item.Receipt = &r
	f.publications[publicationID] = item
	return r, nil
}

func (f *fakePlatformClient) ClaimRestartOperationFence(_ context.Context, operationID, _ string) (apikit.RestartOperationFenceResponse, error) {
	item, ok := f.restarts[operationID]
	if !ok {
		return apikit.RestartOperationFenceResponse{}, platformapi.ErrWorkloadIntentNotFound
	}
	f.restartFenceEpoch++
	return apikit.RestartOperationFenceResponse{FenceEpoch: f.restartFenceEpoch, Intent: item}, nil
}

func (f *fakePlatformClient) RecordRestartComponentCheckpoint(_ context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error) {
	item := f.restarts[operationID]
	if item.ComponentCheckpoints == nil {
		item.ComponentCheckpoints = map[string]apikit.ComponentRestartCheckpoint{}
	}
	item.ComponentCheckpoints[req.Checkpoint.Component] = req.Checkpoint
	f.restarts[operationID] = item
	f.restartCheckpoints = append(f.restartCheckpoints, req)
	return item, nil
}

func (f *fakePlatformClient) RecordRestartOperationReceipt(_ context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	f.restartReceipts = append(f.restartReceipts, req)
	item := f.restarts[operationID]
	if req.Status != "" {
		item.Status = req.Status
	}
	r := apikit.WorkloadExecutionReceipt{
		FenceEpoch:        req.FenceEpoch,
		AppliedRevisionID: req.DesiredRevisionID,
		Outcome:           req.Outcome,
		WorkerID:          req.WorkerID,
	}
	item.Receipt = &r
	f.restarts[operationID] = item
	return r, nil
}

type fakeServerExecutor struct {
	steps                []string
	failOnGrants         bool
	applyResult          ApplyResult
	liveCRUID            string
	deletedUIDs          []string
	replacementProtected bool
}

func (e *fakeServerExecutor) ApplyMCPServer(_ context.Context, _ apikit.ServerIntent) (ApplyResult, error) {
	e.steps = append(e.steps, "apply")
	return e.applyResult, nil
}
func (e *fakeServerExecutor) MarkRetiring(_ context.Context, _, _, targetUID string) (string, error) {
	e.steps = append(e.steps, "mark_retiring")
	if targetUID != "" {
		return targetUID, nil
	}
	return e.liveCRUID, nil
}
func (e *fakeServerExecutor) FenceSessions(_ context.Context, _, _, _ string) error {
	e.steps = append(e.steps, "fence_sessions")
	return nil
}
func (e *fakeServerExecutor) RetireGrants(_ context.Context, _, _ string) error {
	e.steps = append(e.steps, "retire_grants")
	if e.failOnGrants {
		return errors.New("transient grant cleanup failure")
	}
	return nil
}
func (e *fakeServerExecutor) CleanCredentials(_ context.Context, _, _ string) error {
	e.steps = append(e.steps, "clean_credentials")
	return nil
}
func (e *fakeServerExecutor) DeleteMCPServerByUID(_ context.Context, _, _, targetUID string) error {
	e.steps = append(e.steps, "delete_cr:"+targetUID)
	if e.liveCRUID != "" && targetUID != "" && e.liveCRUID != targetUID {
		e.replacementProtected = true
		return nil
	}
	e.deletedUIDs = append(e.deletedUIDs, targetUID)
	return nil
}

func TestServerWorkerAppliesActiveMCPServerAndChecksPublicationDependency(t *testing.T) {
	ctx := context.Background()
	plat := newFakePlatformClient()
	plat.publications["pub-1"] = apikit.ImagePublicationIntent{
		PublicationID: "pub-1",
		Status:        apikit.ImagePublicationStatusPublishing,
	}
	plat.servers["srv-1"] = apikit.ServerIntent{
		ServerID:          "srv-1",
		Namespace:         "mcp-team-alpha",
		Name:              "billing",
		DesiredRevisionID: "rev-1",
		Lifecycle:         apikit.ServerLifecycleActive,
		SpecJSON:          json.RawMessage(`{"image":"registry.local/alpha/billing:v1"}`),
		SpecDigest:        "sha256:spec1",
		PublicationID:     "pub-1",
	}

	exec := &fakeServerExecutor{applyResult: ApplyResult{UID: "uid-cr-1", Generation: 2}}
	worker := NewServerWorker(plat, exec, "worker-1")

	// First run: publication not yet verified -> blocked dependency, no CR apply.
	err := worker.Reconcile(ctx, "srv-1", "rev-1", 1)
	if err == nil {
		t.Fatal("expected retryable dependency error while publication is still Publishing")
	}
	if len(exec.steps) != 0 {
		t.Fatalf("expected 0 apply steps while blocked on publication, got %v", exec.steps)
	}
	if len(plat.serverReceipts) != 1 || plat.serverReceipts[0].Outcome != apikit.WorkloadIntentOutcomeBlockedDependency {
		t.Fatalf("expected BlockedDependency receipt, got %+v", plat.serverReceipts)
	}

	// Publication finishes with verified manifest digest.
	pub := plat.publications["pub-1"]
	pub.Status = apikit.ImagePublicationStatusPublished
	pub.VerifiedDigest = "sha256:verified123"
	plat.publications["pub-1"] = pub

	// Second run: applies MCPServer CR and completes immediately even while rollout is Progressing.
	if err := worker.Reconcile(ctx, "srv-1", "rev-1", 2); err != nil {
		t.Fatalf("Reconcile after publication verified: %v", err)
	}
	if len(exec.steps) != 1 || exec.steps[0] != "apply" {
		t.Fatalf("expected apply step, got %v", exec.steps)
	}
	lastReceipt := plat.serverReceipts[len(plat.serverReceipts)-1]
	if lastReceipt.Outcome != apikit.WorkloadIntentOutcomeApplied || lastReceipt.TargetUID != "uid-cr-1" || lastReceipt.TargetGeneration != 2 {
		t.Fatalf("unexpected applied receipt: %+v", lastReceipt)
	}
}

func TestServerWorkerOrderedRetirementCrashResumeAndReplacementUIDProtection(t *testing.T) {
	ctx := context.Background()
	plat := newFakePlatformClient()
	plat.servers["srv-retire"] = apikit.ServerIntent{
		ServerID:          "srv-retire",
		Namespace:         "mcp-team-alpha",
		Name:              "legacy",
		DesiredRevisionID: "rev-retire-1",
		Lifecycle:         apikit.ServerLifecycleRetiring,
		TargetCRUID:       "uid-old",
	}

	exec := &fakeServerExecutor{failOnGrants: true, liveCRUID: "uid-old"}
	worker := NewServerWorker(plat, exec, "worker-1")

	// Attempt 1 crashes at step 3 (retire_grants).
	if err := worker.Reconcile(ctx, "srv-retire", "rev-retire-1", 1); err == nil {
		t.Fatal("expected error when RetireGrants fails")
	}
	wantFirst := []string{"mark_retiring", "fence_sessions", "retire_grants"}
	if strings.Join(exec.steps, ",") != strings.Join(wantFirst, ",") {
		t.Fatalf("attempt 1 steps = %v, want %v", exec.steps, wantFirst)
	}

	// Attempt 2 resumes after crash: skips RetiringMarked & SessionsFenced!
	// Also simulate a replacement CR appearing with a NEW UID before CR delete.
	exec.steps = nil
	exec.failOnGrants = false
	exec.liveCRUID = "uid-replacement"
	if err := worker.Reconcile(ctx, "srv-retire", "rev-retire-1", 2); err != nil {
		t.Fatalf("attempt 2 Reconcile: %v", err)
	}
	wantSecond := []string{"retire_grants", "clean_credentials", "delete_cr:uid-old"}
	if strings.Join(exec.steps, ",") != strings.Join(wantSecond, ",") {
		t.Fatalf("attempt 2 steps = %v, want %v", exec.steps, wantSecond)
	}
	if !exec.replacementProtected || len(exec.deletedUIDs) != 0 {
		t.Fatalf("replacement CR was not protected: protected=%v deleted=%v", exec.replacementProtected, exec.deletedUIDs)
	}
	if plat.servers["srv-retire"].Lifecycle != apikit.ServerLifecycleRetired {
		t.Fatalf("lifecycle = %q, want Retired", plat.servers["srv-retire"].Lifecycle)
	}
	lastReceipt := plat.serverReceipts[len(plat.serverReceipts)-1]
	if lastReceipt.Outcome != apikit.WorkloadIntentOutcomeCleaned {
		t.Fatalf("last receipt outcome = %q, want Cleaned", lastReceipt.Outcome)
	}
}

func TestDeploymentWorkerAppliesAndUIDConditionallyDeletesRawDeployment(t *testing.T) {
	ctx := context.Background()
	plat := newFakePlatformClient()
	plat.deployments["dep-1"] = apikit.DeploymentIntent{
		DeploymentID:      "dep-1",
		Namespace:         "mcp-team-alpha",
		Name:              "raw-app",
		TeamID:            "team-alpha",
		DesiredRevisionID: "rev-dep-1",
		Deleted:           false,
		Image:             "registry.local/alpha/raw-app:v1",
		Port:              8088,
		Replicas:          2,
		SpecDigest:        "sha256:dep1",
	}

	cs := fake.NewSimpleClientset()
	worker := NewDeploymentWorker(plat, NewKubeDeploymentExecutor(cs), "worker-1")

	if err := worker.Reconcile(ctx, "dep-1", "rev-dep-1", 1); err != nil {
		t.Fatalf("Reconcile apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("mcp-team-alpha").Get(ctx, "raw-app", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get applied deployment: %v", err)
	}
	if _, err := cs.CoreV1().Services("mcp-team-alpha").Get(ctx, "raw-app", metav1.GetOptions{}); err != nil {
		t.Fatalf("get applied service: %v", err)
	}
	if len(plat.deploymentReceipts) != 1 || plat.deploymentReceipts[0].Outcome != apikit.WorkloadIntentOutcomeApplied {
		t.Fatalf("unexpected deployment receipts: %+v", plat.deploymentReceipts)
	}

	// Simulate replacement deployment with a NEW UID; deleting old UID must NOT delete replacement!
	dep.UID = types.UID("uid-replacement")
	if _, err := cs.AppsV1().Deployments("mcp-team-alpha").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update uid: %v", err)
	}
	dIntent := plat.deployments["dep-1"]
	dIntent.Deleted = true
	dIntent.DesiredRevisionID = "rev-dep-2"
	dIntent.TargetDeploymentUID = "uid-original"
	plat.deployments["dep-1"] = dIntent

	if err := worker.Reconcile(ctx, "dep-1", "rev-dep-2", 1); err != nil {
		t.Fatalf("Reconcile delete with mismatched UID: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("mcp-team-alpha").Get(ctx, "raw-app", metav1.GetOptions{}); err != nil {
		t.Fatalf("replacement deployment should still exist after mismatched UID delete: %v", err)
	}
}

type fakeHelperRunner struct {
	pods      map[string]corev1.PodPhase
	created   []string
	waited    []string
	digest    string
	digestErr error
}

func (h *fakeHelperRunner) InspectHelper(_ context.Context, _ string, helperName string) (HelperState, error) {
	phase, ok := h.pods[helperName]
	if !ok {
		return HelperStateNotFound, nil
	}
	switch phase {
	case corev1.PodPending, corev1.PodRunning:
		return HelperStateRunning, nil
	case corev1.PodSucceeded:
		return HelperStateSucceeded, nil
	default:
		return HelperStateFailed, nil
	}
}

func (h *fakeHelperRunner) CreateHelper(_ context.Context, _ apikit.ImagePublicationIntent, helperName string) error {
	h.created = append(h.created, helperName)
	h.pods[helperName] = corev1.PodRunning
	return nil
}

func (h *fakeHelperRunner) WaitHelperSucceeded(_ context.Context, _ string, helperName string) error {
	h.waited = append(h.waited, helperName)
	h.pods[helperName] = corev1.PodSucceeded
	return nil
}

func (h *fakeHelperRunner) VerifyRegistryDigest(_ context.Context, _ apikit.ImagePublicationIntent) (string, error) {
	if h.digestErr != nil {
		return "", h.digestErr
	}
	return h.digest, nil
}

func TestImagePublicationWorkerResumesExistingHelperAndRequiresVerifiedDigest(t *testing.T) {
	ctx := context.Background()
	plat := newFakePlatformClient()
	plat.publications["pub-1"] = apikit.ImagePublicationIntent{
		PublicationID:     "pub-1",
		Namespace:         "mcp-team-alpha",
		Registry:          "registry.local",
		Repository:        "alpha/billing",
		Tag:               "v1",
		UploadState:       apikit.ImageUploadStateFinalized,
		ArtifactRef:       "finalized/pub-1/" + strings.Repeat("a", 64) + ".tar",
		ArtifactSHA256:    strings.Repeat("a", 64),
		ArtifactSizeBytes: 1024,
		DesiredRevisionID: "rev-pub-1",
		Status:            apikit.ImagePublicationStatusQueued,
	}

	expectedHelper := DeterministicHelperName("pub-1", 1)
	runner := &fakeHelperRunner{
		pods:      map[string]corev1.PodPhase{expectedHelper: corev1.PodRunning},
		digestErr: errors.New("registry manifest HEAD 404"),
	}
	worker := NewImagePublicationWorker(plat, runner, "worker-pub-1")

	// Existing helper Pod is Running -> MUST resume it without creating a duplicate helper!
	// Digest verification fails -> MUST NOT mark Published!
	if err := worker.Reconcile(ctx, "pub-1", "rev-pub-1", 1); err == nil {
		t.Fatal("expected error when registry digest verification fails")
	}
	if len(runner.created) != 0 {
		t.Fatalf("expected 0 duplicate helper creations on resume, got %v", runner.created)
	}
	if len(runner.waited) != 1 || runner.waited[0] != expectedHelper {
		t.Fatalf("expected wait on existing helper %q, got %v", expectedHelper, runner.waited)
	}
	if plat.publications["pub-1"].Status == apikit.ImagePublicationStatusPublished {
		t.Fatal("publication MUST NOT be Published without verified manifest digest")
	}

	// Next retry: helper already Succeeded, registry returns verified digest -> Published!
	runner.digestErr = nil
	runner.digest = "sha256:9999abcdef"
	if err := worker.Reconcile(ctx, "pub-1", "rev-pub-1", 1); err != nil {
		t.Fatalf("Reconcile after digest available: %v", err)
	}
	if plat.publications["pub-1"].Status != apikit.ImagePublicationStatusPublished {
		t.Fatalf("status = %q, want Published", plat.publications["pub-1"].Status)
	}
	if plat.publications["pub-1"].VerifiedDigest != "sha256:9999abcdef" {
		t.Fatalf("verified digest = %q, want sha256:9999abcdef", plat.publications["pub-1"].VerifiedDigest)
	}

	// Unfinalized upload must fail terminally without spawning any helper.
	plat.publications["pub-unfinal"] = apikit.ImagePublicationIntent{
		PublicationID:     "pub-unfinal",
		UploadState:       apikit.ImageUploadStateUploading,
		DesiredRevisionID: "rev-u1",
		Status:            apikit.ImagePublicationStatusUploading,
	}
	if err := worker.Reconcile(ctx, "pub-unfinal", "rev-u1", 1); err == nil {
		t.Fatal("expected error for unfinalized upload")
	}
	if len(runner.created) != 0 {
		t.Fatalf("expected 0 helpers for unfinalized upload, got %v", runner.created)
	}
}

type fakeRestartPatcher struct {
	patches   map[string][]string
	failOnce  string
	failedYet bool
}

func (p *fakeRestartPatcher) PatchComponentRestart(_ context.Context, component, restartedAt string) (string, error) {
	if p.patches == nil {
		p.patches = map[string][]string{}
	}
	if component == p.failOnce && !p.failedYet {
		p.failedYet = true
		return "", errors.New("transient api server error")
	}
	p.patches[component] = append(p.patches[component], restartedAt)
	return "uid-" + component, nil
}

func TestRestartWorkerUsesFixedTokenAndResumesPartialCheckpoints(t *testing.T) {
	ctx := context.Background()
	plat := newFakePlatformClient()
	const fixedToken = "2026-11-25T13:00:00Z"
	plat.restarts["op-1"] = apikit.RestartOperationIntent{
		OperationID:       "op-1",
		RequestedBy:       "admin",
		TargetComponents:  []string{"gateway", "ui", "ingest"},
		RestartedAt:       fixedToken,
		DesiredRevisionID: "rev-op-1",
		Status:            apikit.RestartStatusQueued,
	}

	patcher := &fakeRestartPatcher{failOnce: "ui"}
	worker := NewRestartWorker(plat, patcher, "worker-r1")

	// Attempt 1 patches gateway, checkpoints it, then fails on ui.
	if err := worker.Reconcile(ctx, "op-1", "rev-op-1", 1); err == nil {
		t.Fatal("expected transient error on ui")
	}
	if len(patcher.patches["gateway"]) != 1 || patcher.patches["gateway"][0] != fixedToken {
		t.Fatalf("gateway patches = %v, want [%s]", patcher.patches["gateway"], fixedToken)
	}
	if len(patcher.patches["ingest"]) != 0 {
		t.Fatalf("ingest should not have been patched yet: %v", patcher.patches["ingest"])
	}

	// Attempt 2 resumes: MUST skip gateway, patch ui and ingest with the EXACT SAME fixedToken!
	time.Sleep(5 * time.Millisecond)
	if err := worker.Reconcile(ctx, "op-1", "rev-op-1", 2); err != nil {
		t.Fatalf("attempt 2 Reconcile: %v", err)
	}
	if len(patcher.patches["gateway"]) != 1 {
		t.Fatalf("gateway must NOT be re-restarted after checkpoint, got %d patches", len(patcher.patches["gateway"]))
	}
	if len(patcher.patches["ui"]) != 1 || patcher.patches["ui"][0] != fixedToken {
		t.Fatalf("ui patches = %v, want [%s]", patcher.patches["ui"], fixedToken)
	}
	if len(patcher.patches["ingest"]) != 1 || patcher.patches["ingest"][0] != fixedToken {
		t.Fatalf("ingest patches = %v, want [%s]", patcher.patches["ingest"], fixedToken)
	}
	if plat.restarts["op-1"].Status != apikit.RestartStatusCompleted {
		t.Fatalf("restart status = %q, want Completed", plat.restarts["op-1"].Status)
	}
}

func TestRiverConfigUsesDedicatedQueuesAndSchema(t *testing.T) {
	cfg := DefaultRunnerConfig()
	if cfg.Schema != QueueSchema {
		t.Fatalf("Schema = %q, want %q", cfg.Schema, QueueSchema)
	}
	if cfg.WorkloadsMaxWorkers <= 0 || cfg.RegistryPublicationMaxWorkers <= 0 {
		t.Fatalf("expected positive queue budgets, got %+v", cfg)
	}
	queues := cfg.RiverQueues()
	if _, ok := queues[QueueWorkloads]; !ok {
		t.Fatalf("missing queue %q", QueueWorkloads)
	}
	if _, ok := queues[QueueRegistryPublication]; !ok {
		t.Fatalf("missing dedicated queue %q", QueueRegistryPublication)
	}
}

// Keep unused appsv1 import check clean if fake deployment helper references appsv1.
var _ = appsv1.Deployment{}
