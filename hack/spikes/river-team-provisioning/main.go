// Command spike is a throwaway River experiment: team provisioning only.
// It is NOT part of the product build. See README.md.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const schema = `
CREATE TABLE IF NOT EXISTS teams (
  id uuid PRIMARY KEY, slug text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz);
CREATE UNIQUE INDEX IF NOT EXISTS uq_teams_slug ON teams(slug) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS team_provisioning (
  team_id uuid PRIMARY KEY, state text NOT NULL, attempts int NOT NULL DEFAULT 0,
  last_error text, worker text, updated_at timestamptz NOT NULL DEFAULT now());`

// ReconcileArgs carries only the team ID. The worker re-reads desired state,
// so a stale job can never carry stale intent.
type ReconcileArgs struct {
	TeamID string `json:"team_id"`
}

func (ReconcileArgs) Kind() string { return "team_reconcile" }
func (ReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8}
}

type worker struct {
	river.WorkerDefaults[ReconcileArgs]
	pool *pgxpool.Pool
	k8s  kubernetes.Interface
	name string
	safe bool // safe=false reproduces the naive implementation
}

// Short fixed backoff so experiments finish quickly.
func (w *worker) NextRetry(job *river.Job[ReconcileArgs]) time.Time {
	return time.Now().Add(time.Duration(min(job.Attempt, 5)) * 2 * time.Second)
}

func (w *worker) record(ctx context.Context, teamID, state string, attempt int, cause error) {
	msg := sql.NullString{}
	if cause != nil {
		msg = sql.NullString{String: cause.Error(), Valid: true}
	}
	_, err := w.pool.Exec(context.WithoutCancel(ctx), `
INSERT INTO team_provisioning (team_id, state, attempts, last_error, worker)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (team_id) DO UPDATE SET state=$2, attempts=$3, last_error=$4, worker=$5, updated_at=now()`,
		teamID, state, attempt, msg, w.name)
	if err != nil {
		slog.Error("record failed", "err", err)
	}
}

func (w *worker) teamDeleted(ctx context.Context, id string) (slug string, deleted bool, err error) {
	var del sql.NullTime
	err = w.pool.QueryRow(ctx, `SELECT slug, deleted_at FROM teams WHERE id=$1`, id).Scan(&slug, &del)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true, nil
	}
	return slug, del.Valid, err
}

func nsName(slug string) string { return "team-" + slug }

func (w *worker) Work(ctx context.Context, job *river.Job[ReconcileArgs]) error {
	id := job.Args.TeamID
	slog.Info("reconcile start", "worker", w.name, "team", id[:8], "attempt", job.Attempt)
	slug, deleted, err := w.teamDeleted(ctx, id) // read CURRENT desired state
	if err != nil {
		return err
	}
	if deleted {
		return w.deprovision(ctx, id, slug, job.Attempt)
	}
	if d, _ := time.ParseDuration(os.Getenv("SPIKE_DELAY_BEFORE_K8S")); d > 0 {
		slog.Info("injected delay between read and act", "d", d)
		time.Sleep(d)
	}
	created, err := w.ensureNamespace(ctx, id, slug)
	if err != nil {
		w.record(ctx, id, "failed", job.Attempt, err)
		return err
	}
	if os.Getenv("SPIKE_CRASH_AFTER_NS") == "1" {
		slog.Warn("injected crash after namespace creation", "created_now", created)
		os.Exit(137)
	}
	if w.safe { // re-check desired state after acting; converge if it changed
		if _, deleted, err = w.teamDeleted(ctx, id); err != nil {
			return err
		} else if deleted {
			slog.Warn("team deleted during provisioning; rolling back", "team", id[:8])
			return w.deprovision(ctx, id, slug, job.Attempt)
		}
	}
	w.record(ctx, id, "ready", job.Attempt, nil)
	slog.Info("reconcile done", "worker", w.name, "ns", nsName(slug), "namespace_created_by_this_attempt", created)
	return nil
}

// ensureNamespace is idempotent: an existing namespace labelled with our team ID is success;
// one without the label is a conflict we refuse to adopt.
func (w *worker) ensureNamespace(ctx context.Context, id, slug string) (created bool, err error) {
	name := nsName(slug)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"spike/team-id": id}}}
	_, err = w.k8s.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}
	got, gerr := w.k8s.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if gerr != nil {
		return false, gerr
	}
	if got.Labels["spike/team-id"] != id {
		return false, fmt.Errorf("namespace %s exists but belongs to another owner", name)
	}
	return false, nil
}

func (w *worker) deprovision(ctx context.Context, id, slug string, attempt int) error {
	if !w.safe {
		w.record(ctx, id, "skipped-deleted", attempt, nil)
		return nil // naive: no cleanup, deleted teams are simply ignored
	}
	name := nsName(slug)
	got, err := w.k8s.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case got.Labels["spike/team-id"] == id: // only delete what we own
		if err := w.k8s.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	w.record(ctx, id, "deleted", attempt, nil)
	return nil
}

func connect(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("SPIKE_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:spike@127.0.0.1:55432/spike"
	}
	return pgxpool.New(ctx, dsn)
}

func kubeClient() (kubernetes.Interface, error) {
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		return nil, errors.New("KUBECONFIG must be set explicitly")
	}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, nil)
	raw, err := cfg.RawConfig()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(raw.CurrentContext, "kind-") { // prod guardrail
		return nil, fmt.Errorf("refusing non-kind context %q", raw.CurrentContext)
	}
	rest, err := cfg.ClientConfig()
	if err != nil {
		return nil, err
	}
	rest.Timeout = 5 * time.Second
	return kubernetes.NewForConfig(rest)
}

func insertClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{}) // insert-only
}

func createTeam(ctx context.Context, pool *pgxpool.Pool, slug string) (string, error) {
	rc, err := insertClient(pool)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1,$2)`, id, slug); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_provisioning (team_id, state) VALUES ($1,'pending')`, id); err != nil {
		return "", err
	}
	if _, err = rc.InsertTx(ctx, tx, ReconcileArgs{TeamID: id}, nil); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx) // team row + job commit atomically
}

func deleteTeam(ctx context.Context, pool *pgxpool.Pool, slug string, enqueue bool) error {
	rc, err := insertClient(pool)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err = tx.QueryRow(ctx, `UPDATE teams SET deleted_at=now() WHERE slug=$1 AND deleted_at IS NULL RETURNING id`, slug).Scan(&id); err != nil {
		return err
	}
	if enqueue { // safe mode: deletion also schedules convergence
		if _, err = rc.InsertTx(ctx, tx, ReconcileArgs{TeamID: id}, nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func status(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
SELECT t.slug, (t.deleted_at IS NOT NULL), COALESCE(p.state,'-'), COALESCE(p.attempts,0), COALESCE(p.worker,''), COALESCE(left(p.last_error,70),'')
FROM teams t LEFT JOIN team_provisioning p ON p.team_id=t.id ORDER BY t.created_at`)
	if err != nil {
		return err
	}
	fmt.Println("TEAMS: slug deleted state attempts worker last_error")
	for rows.Next() {
		var s, st, w, e string
		var d bool
		var a int
		_ = rows.Scan(&s, &d, &st, &a, &w, &e)
		fmt.Printf("  %-12s %-5v %-16s %d %-4s %s\n", s, d, st, a, w, e)
	}
	rows.Close()
	jr, err := pool.Query(ctx, `
SELECT id, state, attempt, max_attempts, COALESCE(attempted_by[array_length(attempted_by,1)],''), COALESCE(array_length(errors,1),0), args->>'team_id'
FROM river_job WHERE kind='team_reconcile' ORDER BY id`)
	if err != nil {
		return err
	}
	defer jr.Close()
	fmt.Println("RIVER JOBS: id state attempt/max last_worker errors team")
	for jr.Next() {
		var id int64
		var st, w, tm string
		var at, mx, ne int
		_ = jr.Scan(&id, &st, &at, &mx, &w, &ne, &tm)
		fmt.Printf("  %-3d %-10s %d/%d %-8s errs=%d %s\n", id, st, at, mx, w, ne, tm[:8])
	}
	return nil
}

func runWorker(ctx context.Context, pool *pgxpool.Pool, name string, safe bool, concurrency int) error {
	k8s, err := kubeClient()
	if err != nil {
		return err
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker{pool: pool, k8s: k8s, name: name, safe: safe})
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: concurrency}},
		Workers: workers,
		// Default is 1h: a SIGKILLed worker's job would sit "running" that long.
		JobTimeout:           10 * time.Second,
		RescueStuckJobsAfter: 15 * time.Second,
		FetchPollInterval:    500 * time.Millisecond,
		ID:                   name,
	})
	if err != nil {
		return err
	}
	if err := rc.Start(ctx); err != nil {
		return err
	}
	slog.Info("worker started", "name", name, "safe", safe)
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return rc.Stop(stopCtx)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	name := fs.String("name", "w1", "worker name")
	naive := fs.Bool("naive", false, "disable post-check and deletion convergence")
	conc := fs.Int("concurrency", 4, "worker: max concurrent jobs")
	if len(os.Args) > 1 {
		_ = fs.Parse(os.Args[2:])
	}
	pool, err := connect(ctx)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	switch cmd {
	case "migrate":
		var m *rivermigrate.Migrator[pgx.Tx]
		m, err = rivermigrate.New(riverpgxv5.New(pool), nil)
		if err == nil {
			_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
		}
		if err == nil {
			_, err = pool.Exec(ctx, schema)
		}
	case "create-team":
		var id string
		id, err = createTeam(ctx, pool, fs.Arg(0))
		fmt.Println("created team", fs.Arg(0), id)
	case "delete-team":
		err = deleteTeam(ctx, pool, fs.Arg(0), !*naive)
	case "retry": // manual recovery of a discarded job: spike retry <job-id>
		var rc *river.Client[pgx.Tx]
		var id int64
		if rc, err = insertClient(pool); err == nil {
			if _, err = fmt.Sscan(fs.Arg(0), &id); err == nil {
				_, err = rc.JobRetry(ctx, id)
			}
		}
	case "status":
		err = status(ctx, pool)
	case "worker":
		err = runWorker(ctx, pool, *name, !*naive, *conc)
	default:
		err = errors.New("usage: spike migrate|create-team <slug>|delete-team <slug> [-naive]|status|worker [-name w1] [-naive]")
	}
	if err != nil {
		slog.Error(cmd, "err", err)
		os.Exit(1)
	}
}
