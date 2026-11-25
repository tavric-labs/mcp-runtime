// Package accessintent owns PostgreSQL persistence and transactional River
// outbox scheduling for durable access-control intents (grants, adapter
// enrollments, and post-deactivation agent cleanup).
package accessintent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/propagation"

	"mcp-runtime/pkg/httpx"
)

const (
	// QueueSchema is the PostgreSQL schema hosting River tables for access intent jobs.
	QueueSchema = "access_intent_queue"
	// QueueAccessControl is the River queue name for access-control reconciliation jobs.
	QueueAccessControl = "access_control"

	// KindReconcileGrant reconciles a grant incarnation to Kubernetes.
	KindReconcileGrant = "reconcile_grant"
	// KindReconcileEnrollment reconciles an adapter certificate enrollment operation.
	KindReconcileEnrollment = "reconcile_enrollment"
	// KindReconcileAgentCleanup reconciles post-deactivation agent cleanup follow-up.
	KindReconcileAgentCleanup = "reconcile_agent_cleanup"

	defaultClusterKey = "default"
	maxStoredErrorLen = 1024
)

var (
	// ErrDisabled is returned when PLATFORM_ACCESS_INTENT_JOURNAL is not enabled.
	ErrDisabled = errors.New("access intent journal is disabled")
	// ErrStaleReceipt is returned when a worker receipt fails fence, revision,
	// or state ownership validation.
	ErrStaleReceipt = errors.New("stale access intent receipt")
	// ErrStaleObservation is returned when an observer epoch, revision, UID, or
	// propagation assertion fails conditional validation.
	ErrStaleObservation = errors.New("stale access intent observation")
	// ErrAuthorityRevoked is returned when conditional enrollment activation sees
	// a revoked/expired session, inactive agent, or retired/mismatched server.
	ErrAuthorityRevoked = errors.New("enrollment authority revoked or inactive")
	// ErrInvalidInput is returned when an access intent API request is malformed.
	ErrInvalidInput = errors.New("invalid access intent request")
)

const intentSchemaSQL = `
CREATE SCHEMA IF NOT EXISTS access_intent;
CREATE TABLE IF NOT EXISTS access_intent.grant_intent (
	grant_id text PRIMARY KEY,
	namespace text NOT NULL,
	name text NOT NULL,
	team_id text NOT NULL,
	server_name text NOT NULL,
	server_uid text NOT NULL,
	subject_json jsonb NOT NULL DEFAULT '{}'::jsonb,
	spec_json jsonb NOT NULL DEFAULT '{}'::jsonb,
	desired_revision_id text NOT NULL,
	state text NOT NULL,
	revocation_committed_at timestamptz,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_access_grant_intent_ns_name ON access_intent.grant_intent (namespace, name);
CREATE INDEX IF NOT EXISTS idx_access_grant_intent_server_uid ON access_intent.grant_intent (server_uid);
CREATE TABLE IF NOT EXISTS access_intent.grant_receipt (
	grant_id text PRIMARY KEY REFERENCES access_intent.grant_intent(grant_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	target_generation bigint NOT NULL DEFAULT 0,
	spec_digest text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	attempt int NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS access_intent.grant_observation (
	grant_id text PRIMARY KEY REFERENCES access_intent.grant_intent(grant_id) ON DELETE CASCADE,
	observer_id text NOT NULL DEFAULT '',
	observer_epoch bigint NOT NULL DEFAULT 0,
	observed_revision_id text NOT NULL DEFAULT '',
	target_uid text NOT NULL DEFAULT '',
	health text NOT NULL DEFAULT 'Unknown',
	reason text NOT NULL DEFAULT '',
	message text NOT NULL DEFAULT '',
	propagation_status text NOT NULL DEFAULT 'Unknown',
	serving_replicas int NOT NULL DEFAULT 0,
	acknowledged_replicas int NOT NULL DEFAULT 0,
	gateway_evidence jsonb NOT NULL DEFAULT '[]'::jsonb,
	last_transition_at timestamptz NOT NULL,
	last_verified_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS access_intent.enrollment_intent (
	operation_id text PRIMARY KEY,
	session_uid text NOT NULL,
	server_uid text NOT NULL,
	agent_id text NOT NULL DEFAULT '',
	namespace text NOT NULL,
	session_name text NOT NULL,
	csr_sha256 text NOT NULL,
	desired_revision_id text NOT NULL,
	state text NOT NULL,
	certificate_request_name text NOT NULL,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL,
	activated_at timestamptz,
	UNIQUE (session_uid, server_uid, csr_sha256)
);
CREATE TABLE IF NOT EXISTS access_intent.enrollment_receipt (
	operation_id text PRIMARY KEY REFERENCES access_intent.enrollment_intent(operation_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	certificate_request_name text NOT NULL DEFAULT '',
	certificate_request_uid text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	attempt int NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS access_intent.agent_cleanup_intent (
	agent_id text PRIMARY KEY,
	team_id text NOT NULL,
	desired_revision_id text NOT NULL,
	deactivated_at timestamptz NOT NULL,
	revoked_sessions_count bigint NOT NULL DEFAULT 0,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS access_intent.agent_cleanup_receipt (
	agent_id text PRIMARY KEY REFERENCES access_intent.agent_cleanup_intent(agent_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	attempt int NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS access_intent.observer_lease (
	cluster_key text PRIMARY KEY,
	observer_id text NOT NULL,
	observer_epoch bigint NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE SCHEMA IF NOT EXISTS access_intent_queue;
`

// ReconcileGrantJob is the River job payload for grant reconciliation.
type ReconcileGrantJob struct {
	GrantID     string `json:"grantId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileGrantJob) Kind() string { return KindReconcileGrant }

// ReconcileEnrollmentJob is the River job payload for adapter certificate enrollment.
type ReconcileEnrollmentJob struct {
	OperationID string `json:"operationId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileEnrollmentJob) Kind() string { return KindReconcileEnrollment }

// ReconcileAgentCleanupJob is the River job payload for post-deactivation agent cleanup.
type ReconcileAgentCleanupJob struct {
	AgentID     string `json:"agentId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (ReconcileAgentCleanupJob) Kind() string { return KindReconcileAgentCleanup }

func injectTrace(ctx context.Context) (requestID, traceparent, tracestate string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return httpx.RequestID(ctx), carrier.Get("traceparent"), carrier.Get("tracestate")
}

func newReconcileGrantJob(ctx context.Context, grantID, revisionID string) ReconcileGrantJob {
	reqID, tp, ts := injectTrace(ctx)
	return ReconcileGrantJob{
		GrantID:     grantID,
		RevisionID:  revisionID,
		RequestID:   reqID,
		Traceparent: tp,
		Tracestate:  ts,
	}
}

func newReconcileEnrollmentJob(ctx context.Context, opID, revisionID string) ReconcileEnrollmentJob {
	reqID, tp, ts := injectTrace(ctx)
	return ReconcileEnrollmentJob{
		OperationID: opID,
		RevisionID:  revisionID,
		RequestID:   reqID,
		Traceparent: tp,
		Tracestate:  ts,
	}
}

func newReconcileAgentCleanupJob(ctx context.Context, agentID, revisionID string) ReconcileAgentCleanupJob {
	reqID, tp, ts := injectTrace(ctx)
	return ReconcileAgentCleanupJob{
		AgentID:     agentID,
		RevisionID:  revisionID,
		RequestID:   reqID,
		Traceparent: tp,
		Tracestate:  ts,
	}
}

type txJobInserter interface {
	InsertTx(ctx context.Context, tx *sql.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Writer owns access-control durable intent storage and insert-only River job
// scheduling inside PostgreSQL transactions.
type Writer struct {
	db    *sql.DB
	queue txJobInserter
}

// Open prepares storage for the opt-in access intent journal. The DSN must
// point to the same PostgreSQL database as db and support session-level
// advisory locks.
func Open(ctx context.Context, db *sql.DB, dsn string) (*Writer, error) {
	driver := riverdatabasesql.New(db)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: QueueSchema})
	if err != nil {
		return nil, fmt.Errorf("configure access intent migration: %w", err)
	}
	client, err := river.NewClient(driver, &river.Config{Schema: QueueSchema, PollOnly: true})
	if err != nil {
		return nil, fmt.Errorf("configure access intent queue: %w", err)
	}
	// River commits migrations individually. Hold the serialization lock on a
	// separate session so the migrator can use the normal pool even when its
	// maximum connection count is one.
	lockPool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open access intent migration lock pool: %w", err)
	}
	defer lockPool.Close()
	lockPool.SetMaxOpenConns(1)
	lockPool.SetMaxIdleConns(0)
	lockConn, err := lockPool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect for access intent migration lock: %w", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock(873, 2)"); err != nil {
		return nil, fmt.Errorf("serialize access intent migration: %w", err)
	}
	if _, err := lockConn.ExecContext(ctx, intentSchemaSQL); err != nil {
		return nil, fmt.Errorf("create access intent schema: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("migrate access intent queue: %w", err)
	}
	return &Writer{db: db, queue: client}, nil
}

// DeterministicCertificateRequestName computes a stable, DNS-1123 compliant
// Kubernetes CertificateRequest name derived solely from the immutable
// enrollment key (sessionUID, serverUID, csrSHA256).
func DeterministicCertificateRequestName(sessionUID, serverUID, csrSHA256 string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionUID) + "|" + strings.TrimSpace(serverUID) + "|" + strings.ToLower(strings.TrimSpace(csrSHA256))))
	return "enroll-" + hex.EncodeToString(sum[:20])
}

func validateCSRSHA256(raw string) error {
	if len(raw) != 64 {
		return fmt.Errorf("%w: csr_sha256 must be 64 lowercase hex characters", ErrInvalidInput)
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%w: csr_sha256 must be 64 lowercase hex characters", ErrInvalidInput)
		}
	}
	return nil
}

func truncateSanitized(raw string, maxLen int) string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r", " "), "\n", " "))
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}
