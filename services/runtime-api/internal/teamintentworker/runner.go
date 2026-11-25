package teamintentworker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	defaultMaxWorkers           = 4
	defaultJobTimeout           = 20 * time.Second
	defaultRescueStuckJobsAfter = 30 * time.Second
	defaultFetchPollInterval    = 500 * time.Millisecond
	defaultMaxAttempts          = 8
	defaultSoftStopTimeout      = 10 * time.Second
)

// RunnerConfig configures the pgxpool-backed River worker runner.
type RunnerConfig struct {
	DatabaseURL          string
	WorkerID             string
	Platform             PlatformClient
	Reconciler           NamespaceReconciler
	MaxWorkers           int
	JobTimeout           time.Duration
	RescueStuckJobsAfter time.Duration
	FetchPollInterval    time.Duration
	MaxAttempts          int
	SoftStopTimeout      time.Duration
	Logger               *slog.Logger
	TestOnly             bool
}

// Runner owns the PostgreSQL pool and River client for team namespace
// reconciliation.
type Runner struct {
	pool            *pgxpool.Pool
	client          *river.Client[pgx.Tx]
	softStopTimeout time.Duration
}

// NewRunner connects to the River queue database on schema team_intent_queue
// and registers the fenced team namespace worker on queue team_namespace.
func NewRunner(ctx context.Context, cfg RunnerConfig) (*Runner, error) {
	dsn := strings.TrimSpace(cfg.DatabaseURL)
	if dsn == "" {
		return nil, fmt.Errorf("team intent queue database URL is required")
	}
	workerID := strings.TrimSpace(cfg.WorkerID)
	if workerID == "" {
		return nil, fmt.Errorf("team intent worker ID is required")
	}
	if cfg.Platform == nil {
		return nil, fmt.Errorf("platform intent client is required")
	}
	if cfg.Reconciler == nil {
		return nil, fmt.Errorf("namespace reconciler is required")
	}

	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	jobTimeout := cfg.JobTimeout
	if jobTimeout <= 0 {
		jobTimeout = defaultJobTimeout
	}
	rescueAfter := cfg.RescueStuckJobsAfter
	if rescueAfter <= 0 {
		rescueAfter = defaultRescueStuckJobsAfter
	}
	if rescueAfter <= jobTimeout {
		rescueAfter = jobTimeout + 10*time.Second
	}
	pollInterval := cfg.FetchPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultFetchPollInterval
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	softStop := cfg.SoftStopTimeout
	if softStop <= 0 {
		softStop = defaultSoftStopTimeout
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open team intent queue pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping team intent queue database: %w", err)
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, NewWorker(workerID, cfg.Platform, cfg.Reconciler))

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		ID:                   workerID,
		Schema:               QueueSchema,
		Queues:               map[string]river.QueueConfig{QueueName: {MaxWorkers: maxWorkers}},
		Workers:              workers,
		RetryPolicy:          NewRetryPolicy(),
		JobTimeout:           jobTimeout,
		RescueStuckJobsAfter: rescueAfter,
		FetchPollInterval:    pollInterval,
		MaxAttempts:          maxAttempts,
		SoftStopTimeout:      softStop,
		Logger:               cfg.Logger,
		TestOnly:             cfg.TestOnly,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("configure team intent river client: %w", err)
	}

	return &Runner{
		pool:            pool,
		client:          client,
		softStopTimeout: softStop,
	}, nil
}

// Start launches the River worker loop.
func (r *Runner) Start(ctx context.Context) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Start(ctx)
}

// Stop gracefully drains in-flight jobs within SoftStopTimeout and closes the
// PostgreSQL pool.
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var stopErr error
	if r.client != nil {
		stopCtx := ctx
		var cancel context.CancelFunc
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			stopCtx, cancel = context.WithTimeout(ctx, r.softStopTimeout)
			defer cancel()
		}
		stopErr = r.client.Stop(stopCtx)
	}
	if r.pool != nil {
		r.pool.Close()
	}
	return stopErr
}

// RetryJob marks an existing River job immediately eligible for retry.
func (r *Runner) RetryJob(ctx context.Context, jobID int64) (*rivertype.JobRow, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("team intent worker runner not initialized")
	}
	return r.client.JobRetry(ctx, jobID)
}
