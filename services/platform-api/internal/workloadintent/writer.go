// Package workloadintent persists durable desired intent, fenced worker
// execution receipts, independent health observations, and transactional River
// outbox jobs for MCP servers, raw managed deployments, staged image
// publications, and imperative component restarts.
package workloadintent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
	"go.opentelemetry.io/otel/propagation"

	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
)

const (
	// QueueSchema isolates workload intent River tables from other platform tables.
	QueueSchema = "workload_intent_queue"
	// QueueWorkloads carries bounded reconciliation for servers, deployments, and restarts.
	QueueWorkloads = "workloads"
	// QueueRegistryPublication isolates long-running archive publications so transfers
	// cannot starve workload reconciliation.
	QueueRegistryPublication = "registry_publication"

	KindReconcileServer           = "reconcile_server"
	KindReconcileDeployment       = "reconcile_deployment"
	KindReconcileImagePublication = "reconcile_image_publication"
	KindReconcileComponentRestart = "reconcile_component_restart"

	defaultClusterKey = "default"
	maxStoredErrorLen = 1024
)

var (
	// ErrDisabled is returned when PLATFORM_WORKLOAD_INTENT_JOURNAL is not enabled.
	ErrDisabled = errors.New("workload intent journal is disabled")
	// ErrStaleReceipt is returned when a worker receipt or checkpoint fails fence
	// or desired revision validation.
	ErrStaleReceipt = errors.New("stale workload intent receipt")
	// ErrStaleObservation is returned when an observer epoch, revision, or target
	// UID fails conditional validation.
	ErrStaleObservation = errors.New("stale workload intent observation")
	// ErrInvalidInput is returned when a workload intent request is malformed.
	ErrInvalidInput = errors.New("invalid workload intent request")
	// ErrTagOverlapConflict is returned when a mutable-tag publication overlaps an
	// active publication under reject_overlap policy.
	ErrTagOverlapConflict = errors.New("concurrent publication already active for target tag")
	// ErrInvalidStateTransition is returned when an upload or intent transition is
	// not permitted from its current state.
	ErrInvalidStateTransition = errors.New("invalid workload intent state transition")
)

const intentSchema = `
CREATE SCHEMA IF NOT EXISTS workload_intent;
CREATE SCHEMA IF NOT EXISTS workload_intent_queue;

CREATE TABLE IF NOT EXISTS workload_intent.server_intent (
	server_id text PRIMARY KEY,
	namespace text NOT NULL,
	name text NOT NULL,
	team_id text NOT NULL DEFAULT '',
	desired_revision_id text NOT NULL,
	lifecycle text NOT NULL,
	spec_json jsonb NOT NULL DEFAULT '{}'::jsonb,
	spec_digest text NOT NULL DEFAULT '',
	publication_id text NOT NULL DEFAULT '',
	retirement_checkpoints jsonb NOT NULL DEFAULT '{}'::jsonb,
	target_cr_uid text NOT NULL DEFAULT '',
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.server_receipt (
	server_id text PRIMARY KEY REFERENCES workload_intent.server_intent(server_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	target_generation bigint NOT NULL DEFAULT 0,
	spec_digest text NOT NULL DEFAULT '',
	attempt integer NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.server_observation (
	server_id text PRIMARY KEY REFERENCES workload_intent.server_intent(server_id) ON DELETE CASCADE,
	observer_id text NOT NULL DEFAULT '',
	observer_epoch bigint NOT NULL DEFAULT 0,
	observed_revision_id text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	observed_generation bigint NOT NULL DEFAULT 0,
	health text NOT NULL,
	reason text NOT NULL DEFAULT '',
	message text NOT NULL DEFAULT '',
	conditions jsonb NOT NULL DEFAULT '[]'::jsonb,
	last_transition_at timestamptz NOT NULL,
	last_verified_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.deployment_intent (
	deployment_id text PRIMARY KEY,
	namespace text NOT NULL,
	name text NOT NULL,
	team_id text NOT NULL DEFAULT '',
	desired_revision_id text NOT NULL,
	deleted boolean NOT NULL DEFAULT false,
	image text NOT NULL,
	version text NOT NULL DEFAULT '',
	port integer NOT NULL,
	replicas integer NOT NULL,
	spec_digest text NOT NULL DEFAULT '',
	target_deployment_uid text NOT NULL DEFAULT '',
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.deployment_receipt (
	deployment_id text PRIMARY KEY REFERENCES workload_intent.deployment_intent(deployment_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	target_generation bigint NOT NULL DEFAULT 0,
	spec_digest text NOT NULL DEFAULT '',
	attempt integer NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.deployment_observation (
	deployment_id text PRIMARY KEY REFERENCES workload_intent.deployment_intent(deployment_id) ON DELETE CASCADE,
	observer_id text NOT NULL DEFAULT '',
	observer_epoch bigint NOT NULL DEFAULT 0,
	observed_revision_id text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	observed_generation bigint NOT NULL DEFAULT 0,
	health text NOT NULL,
	reason text NOT NULL DEFAULT '',
	message text NOT NULL DEFAULT '',
	conditions jsonb NOT NULL DEFAULT '[]'::jsonb,
	last_transition_at timestamptz NOT NULL,
	last_verified_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.image_publication_intent (
	publication_id text PRIMARY KEY,
	team_id text NOT NULL DEFAULT '',
	namespace text NOT NULL,
	registry text NOT NULL,
	repository text NOT NULL,
	tag text NOT NULL,
	mutable_tag_policy text NOT NULL DEFAULT 'reject_overlap',
	upload_state text NOT NULL,
	artifact_ref text NOT NULL DEFAULT '',
	artifact_sha256 text NOT NULL DEFAULT '',
	artifact_size_bytes bigint NOT NULL DEFAULT 0,
	helper_name text NOT NULL DEFAULT '',
	verified_digest text NOT NULL DEFAULT '',
	desired_revision_id text NOT NULL,
	status text NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_image_publication_target_active
	ON workload_intent.image_publication_intent (registry, repository, tag)
	WHERE status IN ('Uploading', 'Queued', 'Publishing');

CREATE TABLE IF NOT EXISTS workload_intent.image_publication_receipt (
	publication_id text PRIMARY KEY REFERENCES workload_intent.image_publication_intent(publication_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	target_generation bigint NOT NULL DEFAULT 0,
	spec_digest text NOT NULL DEFAULT '',
	attempt integer NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.restart_operation_intent (
	operation_id text PRIMARY KEY,
	requested_by text NOT NULL,
	target_components jsonb NOT NULL DEFAULT '[]'::jsonb,
	restarted_at text NOT NULL,
	component_checkpoints jsonb NOT NULL DEFAULT '{}'::jsonb,
	desired_revision_id text NOT NULL,
	status text NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.restart_operation_receipt (
	operation_id text PRIMARY KEY REFERENCES workload_intent.restart_operation_intent(operation_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	target_generation bigint NOT NULL DEFAULT 0,
	spec_digest text NOT NULL DEFAULT '',
	attempt integer NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workload_intent.observer_lease (
	cluster_key text PRIMARY KEY,
	observer_id text NOT NULL,
	observer_epoch bigint NOT NULL DEFAULT 0,
	updated_at timestamptz NOT NULL
);
`

// ReconcileServerJobArgs is the River payload for MCPServer apply/retirement.
type ReconcileServerJobArgs struct {
	ServerID          string `json:"serverId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileServerJobArgs) Kind() string { return KindReconcileServer }

// ReconcileDeploymentJobArgs is the River payload for raw Deployment/Service apply/delete.
type ReconcileDeploymentJobArgs struct {
	DeploymentID      string `json:"deploymentId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileDeploymentJobArgs) Kind() string { return KindReconcileDeployment }

// ReconcileImagePublicationJobArgs is the River payload for finalized image publication.
type ReconcileImagePublicationJobArgs struct {
	PublicationID     string `json:"publicationId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileImagePublicationJobArgs) Kind() string { return KindReconcileImagePublication }

// ReconcileComponentRestartJobArgs is the River payload for imperative component restarts.
type ReconcileComponentRestartJobArgs struct {
	OperationID       string `json:"operationId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileComponentRestartJobArgs) Kind() string { return KindReconcileComponentRestart }

type jobEnqueuer interface {
	InsertTx(ctx context.Context, tx *sql.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

type riverEnqueuer struct {
	client *river.Client[*sql.Tx]
}

func (r riverEnqueuer) InsertTx(ctx context.Context, tx *sql.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	_, err := r.client.InsertTx(ctx, tx, args, opts)
	return err
}

// Writer owns workload intent persistence and the insert-only River client.
type Writer struct {
	db       *sql.DB
	enqueuer jobEnqueuer
}

func newWriterForTest(db *sql.DB, enq jobEnqueuer) *Writer {
	return &Writer{db: db, enqueuer: enq}
}

// Open prepares storage for the opt-in workload intent journal. The DSN must
// point to the same PostgreSQL database as db and support advisory locks.
func Open(ctx context.Context, db *sql.DB, dsn string) (*Writer, error) {
	driver := riverdatabasesql.New(db)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: QueueSchema})
	if err != nil {
		return nil, fmt.Errorf("configure workload intent migration: %w", err)
	}
	client, err := river.NewClient(driver, &river.Config{Schema: QueueSchema, PollOnly: true})
	if err != nil {
		return nil, fmt.Errorf("configure workload intent queue: %w", err)
	}
	lockPool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open workload intent migration lock pool: %w", err)
	}
	defer lockPool.Close()
	lockPool.SetMaxOpenConns(1)
	lockPool.SetMaxIdleConns(0)
	lockConn, err := lockPool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect for workload intent migration lock: %w", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock(873, 3)"); err != nil {
		return nil, fmt.Errorf("serialize workload intent migration: %w", err)
	}
	if _, err := lockConn.ExecContext(ctx, intentSchema); err != nil {
		return nil, fmt.Errorf("create workload intent schema: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("migrate workload intent queue: %w", err)
	}
	return &Writer{db: db, enqueuer: riverEnqueuer{client: client}}, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func traceHeaders(ctx context.Context) (requestID, traceparent, tracestate string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return httpx.RequestID(ctx), carrier.Get("traceparent"), carrier.Get("tracestate")
}

func validOutcome(v string) bool {
	switch v {
	case apikit.WorkloadIntentOutcomeApplied,
		apikit.WorkloadIntentOutcomeCleaned,
		apikit.WorkloadIntentOutcomeSuperseded,
		apikit.WorkloadIntentOutcomeFailed,
		apikit.WorkloadIntentOutcomeTerminal,
		apikit.WorkloadIntentOutcomeBlockedDependency:
		return true
	default:
		return false
	}
}

func validHealth(v string) bool {
	switch v {
	case apikit.WorkloadIntentHealthPending,
		apikit.WorkloadIntentHealthProgressing,
		apikit.WorkloadIntentHealthReady,
		apikit.WorkloadIntentHealthDegraded,
		apikit.WorkloadIntentHealthDeleted,
		apikit.WorkloadIntentHealthUnknown:
		return true
	default:
		return false
	}
}

func truncateSanitized(raw string, maxLen int) string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r", " "), "\n", " "))
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeSpecJSON(raw json.RawMessage) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []byte("{}"), nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, fmt.Errorf("%w: invalid spec_json", ErrInvalidInput)
	}
	return json.Marshal(decoded)
}

func buildReceipt(
	hasReceipt bool,
	receipt apikit.WorkloadExecutionReceipt,
	appliedAt, updatedAt sql.NullTime,
) *apikit.WorkloadExecutionReceipt {
	if !hasReceipt {
		return nil
	}
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		receipt.AppliedAt = &t
	}
	if updatedAt.Valid {
		receipt.UpdatedAt = updatedAt.Time.UTC()
	}
	return &receipt
}

func buildObservation(
	hasObs bool,
	obs apikit.WorkloadStatusObservation,
	condsJSON []byte,
	transitionAt, verifiedAt, updatedAt sql.NullTime,
) *apikit.WorkloadStatusObservation {
	if !hasObs {
		return nil
	}
	if len(condsJSON) > 0 {
		_ = json.Unmarshal(condsJSON, &obs.Conditions)
	}
	if transitionAt.Valid {
		obs.LastTransitionAt = transitionAt.Time.UTC()
	}
	if verifiedAt.Valid {
		obs.LastVerifiedAt = verifiedAt.Time.UTC()
	}
	if updatedAt.Valid {
		obs.UpdatedAt = updatedAt.Time.UTC()
	}
	return &obs
}
