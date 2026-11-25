// Package teamintent records team namespace intent in the platform transaction.
package teamintent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
	"go.opentelemetry.io/otel/propagation"
	"mcp-runtime/pkg/httpx"
)

const queueSchema = "team_intent_queue"

const intentSchema = `
CREATE SCHEMA IF NOT EXISTS team_intent;
CREATE TABLE IF NOT EXISTS team_intent.team_namespace_intent (
	team_id text PRIMARY KEY,
	revision_id text NOT NULL,
	deleted boolean NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS team_intent.team_namespace_receipt (
	team_id text PRIMARY KEY REFERENCES team_intent.team_namespace_intent(team_id) ON DELETE CASCADE,
	fence_epoch bigint NOT NULL DEFAULT 0,
	fence_holder text NOT NULL DEFAULT '',
	applied_revision_id text NOT NULL DEFAULT '',
	outcome text NOT NULL DEFAULT '',
	namespace_uid text NOT NULL DEFAULT '',
	attempt int NOT NULL DEFAULT 0,
	worker_id text NOT NULL DEFAULT '',
	last_error_class text NOT NULL DEFAULT '',
	last_error text NOT NULL DEFAULT '',
	applied_at timestamptz,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS team_intent.team_namespace_observation (
	team_id text PRIMARY KEY REFERENCES team_intent.team_namespace_intent(team_id) ON DELETE CASCADE,
	observer_id text NOT NULL DEFAULT '',
	observer_epoch bigint NOT NULL DEFAULT 0,
	observed_revision_id text NOT NULL DEFAULT '',
	namespace_uid text NOT NULL DEFAULT '',
	health text NOT NULL DEFAULT 'Unknown',
	reason text NOT NULL DEFAULT '',
	message text NOT NULL DEFAULT '',
	conditions jsonb NOT NULL DEFAULT '[]'::jsonb,
	last_transition_at timestamptz NOT NULL,
	last_verified_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS team_intent.observer_lease (
	cluster_key text PRIMARY KEY,
	observer_id text NOT NULL,
	observer_epoch bigint NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE SCHEMA IF NOT EXISTS team_intent_queue;
`

type reconcileJob struct {
	TeamID      string `json:"teamId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func (reconcileJob) Kind() string { return "reconcile_team_namespace" }

func newReconcileJob(ctx context.Context, teamID, revisionID string) reconcileJob {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return reconcileJob{
		TeamID: teamID, RevisionID: revisionID, RequestID: httpx.RequestID(ctx),
		Traceparent: carrier.Get("traceparent"), Tracestate: carrier.Get("tracestate"),
	}
}

// Writer owns the insert-only River client and team intent persistence.
type Writer struct {
	db    *sql.DB
	queue *river.Client[*sql.Tx]
}

// Open prepares storage for the opt-in team journal. The DSN must point to the
// same PostgreSQL database as db and support session-level advisory locks.
func Open(ctx context.Context, db *sql.DB, dsn string) (*Writer, error) {
	driver := riverdatabasesql.New(db)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: queueSchema})
	if err != nil {
		return nil, fmt.Errorf("configure team intent migration: %w", err)
	}
	client, err := river.NewClient(driver, &river.Config{Schema: queueSchema, PollOnly: true})
	if err != nil {
		return nil, fmt.Errorf("configure team intent queue: %w", err)
	}
	// River commits migrations individually. Hold the serialization lock
	// on a separate session so the migrator can use the normal pool even
	// when its maximum connection count is one.
	lockPool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open team intent migration lock pool: %w", err)
	}
	defer lockPool.Close()
	lockPool.SetMaxOpenConns(1)
	lockPool.SetMaxIdleConns(0)
	lockConn, err := lockPool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect for team intent migration lock: %w", err)
	}
	// With no idle connections, closing this connection closes the session
	// and releases its advisory lock even if startup is canceled.
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock(873, 1)"); err != nil {
		return nil, fmt.Errorf("serialize team intent migration: %w", err)
	}
	if _, err := lockConn.ExecContext(ctx, intentSchema); err != nil {
		return nil, fmt.Errorf("create team intent schema: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("migrate team intent queue: %w", err)
	}
	return &Writer{db: db, queue: client}, nil
}

// Record stores the latest desired revision for the team ID and schedules its
// job using the same transaction that changes the team. The caller owns commit
// and rollback.
func (w *Writer) Record(ctx context.Context, tx *sql.Tx, teamID string, deleted bool) error {
	revisionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_intent.team_namespace_intent (team_id, revision_id, deleted, updated_at)
		VALUES ($1, $2, $3, clock_timestamp())
		ON CONFLICT (team_id) DO UPDATE SET
			revision_id = EXCLUDED.revision_id,
			deleted = EXCLUDED.deleted,
			updated_at = EXCLUDED.updated_at
	`, teamID, revisionID, deleted); err != nil {
		return fmt.Errorf("record team namespace intent: %w", err)
	}
	_, err := w.queue.InsertTx(ctx, tx, newReconcileJob(ctx, teamID, revisionID), &river.InsertOpts{Queue: "team_namespace"})
	if err != nil {
		return fmt.Errorf("enqueue team namespace reconciliation: %w", err)
	}
	return nil
}
