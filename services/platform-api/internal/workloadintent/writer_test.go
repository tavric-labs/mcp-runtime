package workloadintent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/lib/pq"
	"github.com/riverqueue/river"

	"mcp-runtime/pkg/apikit"
)

type enqueuedJob struct {
	args  river.JobArgs
	queue string
}

type fakeEnqueuer struct {
	jobs []enqueuedJob
	err  error
}

func (f *fakeEnqueuer) InsertTx(_ context.Context, _ *sql.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	if f.err != nil {
		return f.err
	}
	queue := ""
	if opts != nil {
		queue = opts.Queue
	}
	f.jobs = append(f.jobs, enqueuedJob{args: args, queue: queue})
	return nil
}

func TestBeginPublicationDoesNotEnqueueAndRejectsMutableTagOverlap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	enq := &fakeEnqueuer{}
	w := newWriterForTest(db, enq)
	now := time.Now().UTC()

	// Case 1: Active overlap exists -> reject with ErrTagOverlapConflict and no enqueue.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT publication_id FROM workload_intent\.image_publication_intent`).
		WithArgs("registry.local", "alpha/billing", "latest").
		WillReturnRows(sqlmock.NewRows([]string{"publication_id"}).AddRow("pub-existing"))
	mock.ExpectRollback()

	_, err = w.BeginImagePublication(context.Background(), apikit.ImagePublicationBeginRequest{
		PublicationID: "pub-new",
		Namespace:     "mcp-team-alpha",
		Registry:      "registry.local",
		Repository:    "alpha/billing",
		Tag:           "latest",
	})
	if !errors.Is(err, ErrTagOverlapConflict) {
		t.Fatalf("BeginImagePublication overlap = %v, want ErrTagOverlapConflict", err)
	}
	if len(enq.jobs) != 0 {
		t.Fatalf("expected 0 enqueued jobs on overlap conflict, got %d", len(enq.jobs))
	}

	// Case 2: No overlap -> inserts Uploading row, commits, and STILL enqueues ZERO River jobs.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT publication_id FROM workload_intent\.image_publication_intent`).
		WithArgs("registry.local", "alpha/billing", "v1").
		WillReturnRows(sqlmock.NewRows([]string{"publication_id"}))
	mock.ExpectQuery(`INSERT INTO workload_intent\.image_publication_intent`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))
	mock.ExpectCommit()

	got, err := w.BeginImagePublication(context.Background(), apikit.ImagePublicationBeginRequest{
		PublicationID: "pub-v1",
		Namespace:     "mcp-team-alpha",
		Registry:      "registry.local",
		Repository:    "alpha/billing",
		Tag:           "v1",
	})
	if err != nil {
		t.Fatalf("BeginImagePublication: %v", err)
	}
	if got.UploadState != apikit.ImageUploadStateUploading || got.Status != apikit.ImagePublicationStatusUploading {
		t.Fatalf("unexpected begin state: %+v", got)
	}
	if len(enq.jobs) != 0 {
		t.Fatalf("BeginImagePublication must NOT enqueue River jobs, got %d", len(enq.jobs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestFinalizePublicationEnqueuesOnDedicatedRegistryPublicationQueue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	enq := &fakeEnqueuer{}
	w := newWriterForTest(db, enq)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT upload_state, status FROM workload_intent\.image_publication_intent WHERE publication_id = \$1 FOR UPDATE`).
		WithArgs("pub-v1").
		WillReturnRows(sqlmock.NewRows([]string{"upload_state", "status"}).AddRow(apikit.ImageUploadStateUploading, apikit.ImagePublicationStatusUploading))
	mock.ExpectExec(`UPDATE workload_intent\.image_publication_intent`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM workload_intent\.image_publication_intent i`).
		WithArgs("pub-v1").
		WillReturnRows(sqlmock.NewRows([]string{
			"publication_id", "team_id", "namespace", "registry", "repository", "tag",
			"mutable_tag_policy", "upload_state", "artifact_ref", "artifact_sha256",
			"artifact_size_bytes", "helper_name", "verified_digest", "desired_revision_id",
			"status", "updated_at",
			"has_receipt", "fence_epoch", "fence_holder", "applied_revision_id", "outcome",
			"target_uid", "target_generation", "spec_digest", "attempt", "worker_id",
			"last_error_class", "last_error", "applied_at", "receipt_updated_at",
		}).AddRow(
			"pub-v1", "team-1", "mcp-team-alpha", "registry.local", "alpha/billing", "v1",
			apikit.MutableTagPolicyRejectOverlap, apikit.ImageUploadStateFinalized,
			"finalized/pub-v1/"+strings.Repeat("a", 64)+".tar", strings.Repeat("a", 64),
			int64(4096), "", "", "rev-final-1",
			apikit.ImagePublicationStatusQueued, now,
			false, int64(0), "", "", "", "", int64(0), "", 0, "", "", "", nil, nil,
		))
	mock.ExpectCommit()

	got, err := w.FinalizeImagePublication(context.Background(), "pub-v1", apikit.ImagePublicationFinalizeRequest{
		ArtifactRef:       "finalized/pub-v1/" + strings.Repeat("a", 64) + ".tar",
		ArtifactSHA256:    strings.Repeat("a", 64),
		ArtifactSizeBytes: 4096,
	})
	if err != nil {
		t.Fatalf("FinalizeImagePublication: %v", err)
	}
	if got.UploadState != apikit.ImageUploadStateFinalized || got.Status != apikit.ImagePublicationStatusQueued {
		t.Fatalf("unexpected finalized publication: %+v", got)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("expected 1 enqueued job, got %d", len(enq.jobs))
	}
	if enq.jobs[0].queue != QueueRegistryPublication {
		t.Fatalf("queue = %q, want %q", enq.jobs[0].queue, QueueRegistryPublication)
	}
	if enq.jobs[0].args.Kind() != KindReconcileImagePublication {
		t.Fatalf("job kind = %q, want %q", enq.jobs[0].args.Kind(), KindReconcileImagePublication)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestCreateRestartOperationPersistsFixedTimestampAndEnqueuesOnWorkloadsQueue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	enq := &fakeEnqueuer{}
	w := newWriterForTest(db, enq)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO workload_intent\.restart_operation_intent`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))
	mock.ExpectCommit()

	got, err := w.CreateRestartOperation(context.Background(), apikit.RestartOperationCreateRequest{
		OperationID:      "op-restart-1",
		RequestedBy:      "admin-1",
		TargetComponents: []string{"gateway", "ui", "gateway"},
		RestartedAt:      "2026-11-25T13:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateRestartOperation: %v", err)
	}
	if got.RestartedAt != "2026-11-25T13:00:00Z" {
		t.Fatalf("RestartedAt = %q, want fixed token", got.RestartedAt)
	}
	if len(got.TargetComponents) != 2 || got.TargetComponents[0] != "gateway" || got.TargetComponents[1] != "ui" {
		t.Fatalf("expected deduped ordered TargetComponents, got %v", got.TargetComponents)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].queue != QueueWorkloads || enq.jobs[0].args.Kind() != KindReconcileComponentRestart {
		t.Fatalf("unexpected enqueued job: %+v", enq.jobs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestUpsertServerAndDeploymentEnqueueOnWorkloadsQueue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	enq := &fakeEnqueuer{}
	w := newWriterForTest(db, enq)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO workload_intent\.server_intent`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))
	mock.ExpectCommit()

	srv, err := w.UpsertServerIntent(context.Background(), apikit.ServerIntentUpsertRequest{
		ServerID:  "srv-1",
		Namespace: "mcp-team-alpha",
		Name:      "billing",
		SpecJSON:  json.RawMessage(`{"image":"registry.local/alpha/billing:v1"}`),
	})
	if err != nil {
		t.Fatalf("UpsertServerIntent: %v", err)
	}
	if srv.Lifecycle != apikit.ServerLifecycleActive || srv.SpecDigest == "" || srv.DesiredRevisionID == "" {
		t.Fatalf("unexpected server intent: %+v", srv)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO workload_intent\.deployment_intent`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))
	mock.ExpectCommit()

	dep, err := w.UpsertDeploymentIntent(context.Background(), apikit.DeploymentIntentUpsertRequest{
		DeploymentID: "dep-1",
		Namespace:    "mcp-team-alpha",
		Name:         "raw-web",
		Image:        "registry.local/alpha/raw-web:v1",
		Port:         8088,
		Replicas:     2,
	})
	if err != nil {
		t.Fatalf("UpsertDeploymentIntent: %v", err)
	}
	if dep.Deleted || dep.SpecDigest == "" {
		t.Fatalf("unexpected deployment intent: %+v", dep)
	}
	if len(enq.jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(enq.jobs))
	}
	if enq.jobs[0].queue != QueueWorkloads || enq.jobs[0].args.Kind() != KindReconcileServer {
		t.Fatalf("unexpected job 0: %+v", enq.jobs[0])
	}
	if enq.jobs[1].queue != QueueWorkloads || enq.jobs[1].args.Kind() != KindReconcileDeployment {
		t.Fatalf("unexpected job 1: %+v", enq.jobs[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestRecordPublicationReceiptRequiresVerifiedDigest(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	w := newWriterForTest(db, &fakeEnqueuer{})
	_, err = w.RecordImagePublicationReceipt(context.Background(), "pub-1", apikit.ImagePublicationReceiptRequest{
		DesiredRevisionID: "rev-1",
		FenceEpoch:        1,
		Outcome:           apikit.WorkloadIntentOutcomeApplied,
		Status:            apikit.ImagePublicationStatusPublished,
		HelperName:        "mcp-push-pub-1-a1",
		VerifiedDigest:    "",
		WorkerID:          "worker-1",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RecordImagePublicationReceipt without digest = %v, want ErrInvalidInput", err)
	}
}

func TestServerRetirementCheckpointRejectsStaleFence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	w := newWriterForTest(db, &fakeEnqueuer{})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT i\.desired_revision_id, i\.lifecycle, i\.target_cr_uid, i\.retirement_checkpoints`).
		WithArgs("srv-1").
		WillReturnRows(sqlmock.NewRows([]string{"desired_revision_id", "lifecycle", "target_cr_uid", "retirement_checkpoints", "fence_epoch"}).
			AddRow("rev-1", apikit.ServerLifecycleRetiring, "uid-1", []byte(`{}`), int64(3)))
	mock.ExpectRollback()

	_, err = w.RecordServerRetirementCheckpoint(context.Background(), "srv-1", apikit.ServerRetirementCheckpointRequest{
		DesiredRevisionID: "rev-1",
		FenceEpoch:        2, // stale
		WorkerID:          "worker-old",
		Checkpoints:       apikit.ServerRetirementCheckpoints{RetiringMarked: true},
	})
	if !errors.Is(err, ErrStaleReceipt) {
		t.Fatalf("stale checkpoint err = %v, want ErrStaleReceipt", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestWorkloadIntentPostgresIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MCP_PLATFORM_WORKLOAD_INTENT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MCP_PLATFORM_WORKLOAD_INTENT_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	w, err := Open(ctx, db, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	srv, err := w.UpsertServerIntent(ctx, apikit.ServerIntentUpsertRequest{
		ServerID:  "pg-srv-1",
		Namespace: "mcp-team-alpha",
		Name:      "billing",
		TeamID:    "team-alpha",
		SpecJSON:  json.RawMessage(`{"image":"registry.local/alpha/billing:v1"}`),
	})
	if err != nil {
		t.Fatalf("UpsertServerIntent: %v", err)
	}
	claim, err := w.ClaimServerFence(ctx, srv.ServerID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimServerFence: %v", err)
	}
	if _, err := w.RecordServerReceipt(ctx, srv.ServerID, apikit.ServerIntentReceiptRequest{
		DesiredRevisionID: claim.Intent.DesiredRevisionID,
		FenceEpoch:        claim.FenceEpoch,
		Outcome:           apikit.WorkloadIntentOutcomeApplied,
		TargetUID:         "uid-1",
		TargetGeneration:  1,
		SpecDigest:        claim.Intent.SpecDigest,
		WorkerID:          "worker-a",
	}); err != nil {
		t.Fatalf("RecordServerReceipt: %v", err)
	}
	lease, err := w.ClaimObserverLease(ctx, "default", "obs-a")
	if err != nil {
		t.Fatalf("ClaimObserverLease: %v", err)
	}
	obs, err := w.RecordServerObservation(ctx, srv.ServerID, apikit.ServerIntentObservationRequest{
		ClusterKey:         "default",
		ObserverID:         "obs-a",
		ObserverEpoch:      lease.ObserverEpoch,
		ObservedRevisionID: claim.Intent.DesiredRevisionID,
		TargetUID:          "uid-1",
		ObservedGeneration: 1,
		Health:             apikit.WorkloadIntentHealthReady,
	})
	if err != nil {
		t.Fatalf("RecordServerObservation: %v", err)
	}
	if obs.Health != apikit.WorkloadIntentHealthReady {
		t.Fatalf("health = %q, want Ready", obs.Health)
	}
}
