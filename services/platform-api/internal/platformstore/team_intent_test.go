package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"go.opentelemetry.io/otel/propagation"
	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/httpx"
)

func TestPostgresTeamIntentDisabledByDefault(t *testing.T) {
	s := openTeamIntentTestStore(t, "")
	team := createTeamIntentFixture(t, s, t.Context(), "legacy-team")
	assertTeamIntentDomainState(t, s, team, false)
	if err := s.DeleteTeamBySlug(t.Context(), team.Slug); err != nil {
		t.Fatal(err)
	}
	assertTeamIntentDomainState(t, s, team, true)
	for _, table := range []string{"team_intent.team_namespace_intent", "team_intent_queue.river_job"} {
		var exists bool
		if err := s.db.QueryRowContext(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("legacy startup initialized %s", table)
		}
	}
}

func TestPostgresTeamIntentCommitsDistinctCreateAndDeleteRevisions(t *testing.T) {
	s := openTeamIntentTestStore(t, "true")
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const tracestate = "vendor=team-fixture"
	ctx := propagation.TraceContext{}.Extract(t.Context(), propagation.HeaderCarrier(http.Header{
		"Traceparent": {traceparent},
		"Tracestate":  {tracestate},
	}))
	team := createTeamIntentFixture(t, s, httpx.ContextWithRequestID(ctx, "team-fixture-create"), "intent-team")
	assertTeamIntentDomainState(t, s, team, false)
	createRevision := checkTeamNamespaceIntent(t, s, team.ID, false)
	assertTeamIntentJobs(t, s, team.ID, []teamIntentJobPayload{{
		revision: createRevision, requestID: "team-fixture-create", traceparent: traceparent, tracestate: tracestate,
	}})
	if err := s.DeleteTeamBySlug(httpx.ContextWithRequestID(t.Context(), "team-fixture-delete"), team.Slug); err != nil {
		t.Fatal(err)
	}
	assertTeamIntentDomainState(t, s, team, true)
	deleteRevision := checkTeamNamespaceIntent(t, s, team.ID, true)
	if deleteRevision == createRevision {
		t.Fatalf("delete reused create revision %q", createRevision)
	}
	assertTeamIntentJobs(t, s, team.ID, []teamIntentJobPayload{
		{revision: createRevision, requestID: "team-fixture-create", traceparent: traceparent, tracestate: tracestate},
		{revision: deleteRevision, requestID: "team-fixture-delete"},
	})
}

func TestPostgresTeamIntentQueueFailureRollsBackDomainChanges(t *testing.T) {
	s := openTeamIntentTestStore(t, "true")
	ctx := t.Context()
	team := createTeamIntentFixture(t, s, ctx, "delete-rolls-back")
	createRevision := checkTeamNamespaceIntent(t, s, team.ID, false)
	owner, err := s.CreatePasswordUser(ctx, "create-rollback@fixture.test", "fixture-password-only", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
CREATE FUNCTION team_intent_queue.team_intent_test_reject_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'team intent test rejected River insert';
END;
$$;
CREATE TRIGGER team_intent_test_reject_job
BEFORE INSERT ON team_intent_queue.river_job
FOR EACH ROW EXECUTE FUNCTION team_intent_queue.team_intent_test_reject_job();`); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateTeam(ctx, "create-rolls-back", "Rollback fixture", owner.ID)
	assertTeamIntentInsertFailure(t, err)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM teams`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM namespaces WHERE scope = 'team'`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_memberships`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent.team_namespace_intent`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 1)

	assertTeamIntentInsertFailure(t, s.DeleteTeamBySlug(ctx, team.Slug))
	assertTeamIntentDomainState(t, s, team, false)
	if revision := checkTeamNamespaceIntent(t, s, team.ID, false); revision != createRevision {
		t.Fatalf("failed delete changed revision from %q to %q", createRevision, revision)
	}
	assertTeamIntentJobs(t, s, team.ID, []teamIntentJobPayload{{revision: createRevision}})
}

func TestPostgresTeamIntentConcurrentDeletesQueueOneRevision(t *testing.T) {
	s := openTeamIntentTestStore(t, "true")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	team := createTeamIntentFixture(t, s, ctx, "concurrent-delete")
	createRevision := checkTeamNamespaceIntent(t, s, team.ID, false)
	lock, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, `SELECT 1 FROM team_memberships WHERE team_id = $1 FOR UPDATE`, team.ID); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range cap(results) {
		go func() { results <- s.DeleteTeamBySlug(ctx, team.Slug) }()
	}
	// Both callers must read the active team before the first delete may commit.
	// Hold the first row each delete updates until both queries are waiting.
	for {
		var waiters int
		if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE 'UPDATE team_memberships SET deleted_at = %'`).Scan(&waiters); err != nil {
			t.Fatalf("wait for concurrent deletes to reach membership lock: %v", err)
		}
		if waiters == cap(results) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := lock.Commit(); err != nil {
		t.Fatal(err)
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertTeamIntentDomainState(t, s, team, true)
	deleteRevision := checkTeamNamespaceIntent(t, s, team.ID, true)
	assertTeamIntentJobs(t, s, team.ID, []teamIntentJobPayload{{revision: createRevision}, {revision: deleteRevision}})
}

func TestPostgresTeamIntentConcurrentJournalStartup(t *testing.T) {
	dsn := teamIntentTestDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	results := make(chan error, 2)
	start := make(chan struct{})
	for range cap(results) {
		go func() {
			<-start
			_, err := teamintent.Open(ctx, db, dsn)
			results <- err
		}()
	}
	close(start)
	for range cap(results) {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

func openTeamIntentTestStore(t *testing.T, enabled string) *Store {
	t.Helper()
	dsn := teamIntentTestDatabase(t)
	t.Setenv("PLATFORM_TEAM_INTENT_JOURNAL", enabled)
	s, err := Open(t.Context(), dsn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func teamIntentTestDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MCP_PLATFORM_TEAM_INTENT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MCP_PLATFORM_TEAM_INTENT_TEST_POSTGRES_DSN to disposable local PostgreSQL with CREATE DATABASE privilege")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("integration database must use a localhost PostgreSQL URL")
	}
	if query := u.Query(); query.Has("host") || query.Has("hostaddr") || query.Has("dbname") || query.Has("service") {
		t.Fatal("integration database URL must use the hostname and database in its authority and path")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	database := "team_intent_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+pq.QuoteIdentifier(database)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE `+pq.QuoteIdentifier(database)); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + database
	return u.String()
}

func createTeamIntentFixture(t *testing.T, s *Store, ctx context.Context, slug string) Team {
	t.Helper()
	owner, err := s.CreatePasswordUser(t.Context(), "owner@fixture.test", "fixture-password-only", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, slug, "Team fixture", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	return team
}

func assertTeamIntentDomainState(t *testing.T, s *Store, team Team, deleted bool) {
	t.Helper()
	var teamDeleted, namespaceDeleted, ownerDeleted bool
	err := s.db.QueryRowContext(t.Context(), `
SELECT t.deleted_at IS NOT NULL, n.deleted_at IS NOT NULL, m.deleted_at IS NOT NULL
FROM teams t
JOIN namespaces n ON n.team_id = t.id AND n.namespace = $2
JOIN team_memberships m ON m.team_id = t.id AND m.role = 'owner'
WHERE t.id = $1`, team.ID, team.Namespace).Scan(&teamDeleted, &namespaceDeleted, &ownerDeleted)
	if err != nil {
		t.Fatal(err)
	}
	if teamDeleted != deleted || namespaceDeleted != deleted || ownerDeleted != deleted {
		t.Fatalf("expected all record deletion flags to be %t, got team=%t namespace=%t membership=%t", deleted, teamDeleted, namespaceDeleted, ownerDeleted)
	}
}

func checkTeamNamespaceIntent(t *testing.T, s *Store, teamID string, deleted bool) string {
	t.Helper()
	var revision string
	var actualDeleted bool
	err := s.db.QueryRowContext(t.Context(), `
SELECT revision_id, deleted FROM team_intent.team_namespace_intent WHERE team_id = $1`, teamID).Scan(&revision, &actualDeleted)
	if err != nil {
		t.Fatal(err)
	}
	if revision == "" || actualDeleted != deleted {
		t.Fatalf("unexpected desired intent: revision=%q deleted=%t, want deleted=%t", revision, actualDeleted, deleted)
	}
	return revision
}

type teamIntentJobPayload struct {
	revision, requestID, traceparent, tracestate string
}

func assertTeamIntentJobs(t *testing.T, s *Store, teamID string, expected []teamIntentJobPayload) {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `
SELECT args->>'revisionId', COALESCE(args->>'requestId', ''), COALESCE(args->>'traceparent', ''), COALESCE(args->>'tracestate', '')
FROM team_intent_queue.river_job
WHERE kind = 'reconcile_team_namespace' AND queue = 'team_namespace' AND args->>'teamId' = $1
ORDER BY id`, teamID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []teamIntentJobPayload
	for rows.Next() {
		var job teamIntentJobPayload
		if err := rows.Scan(&job.revision, &job.requestID, &job.traceparent, &job.tracestate); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, job)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("jobs = %+v, want %+v", actual, expected)
	}
	for i, job := range actual {
		if job != expected[i] {
			t.Fatalf("job %d = %+v, want %+v", i, job, expected[i])
		}
	}
}

func assertTeamIntentCount(t *testing.T, s *Store, query string, expected int) {
	t.Helper()
	var actual int
	if err := s.db.QueryRowContext(t.Context(), query).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("count = %d, want %d for %q", actual, expected, query)
	}
}

func assertTeamIntentInsertFailure(t *testing.T, err error) {
	t.Helper()
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) || pgErr.Message != "team intent test rejected River insert" {
		t.Fatalf("expected injected River insert failure, got %v", err)
	}
}
