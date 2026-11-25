package accessintentworker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"k8s.io/client-go/dynamic"

	"mcp-runtime-api/internal/platformapi"
)

// RunnerConfig configures the opt-in runtime-api River worker pool for access intents.
type RunnerConfig struct {
	PostgresDSN    string
	PlatformClient *platformapi.Client
	DynamicClient  dynamic.Interface
	ClusterIssuer  string
	WorkerID       string
	MaxWorkers     int
	CSRProvider    CSRProvider
	CleanupExec    AgentCleanupExecutor
}

// Runner owns the River pgxv5 client and worker lifecycle for access_control jobs.
type Runner struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
}

// Start opens the pgxpool connection, registers all access intent workers on
// the access_control queue in schema access_intent_queue, and starts River.
func Start(ctx context.Context, cfg RunnerConfig) (*Runner, error) {
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, errors.New("access intent worker requires PostgreSQL DSN")
	}
	if cfg.PlatformClient == nil || !cfg.PlatformClient.Configured() {
		return nil, errors.New("access intent worker requires configured platform-api client")
	}
	if cfg.DynamicClient == nil {
		return nil, errors.New("access intent worker requires Kubernetes dynamic client")
	}
	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 4
	}
	workerID := strings.TrimSpace(cfg.WorkerID)
	if workerID == "" {
		workerID = "runtime-api-access-worker"
	}

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("connect access intent worker pool: %w", err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &GrantWorker{
		Platform:  cfg.PlatformClient,
		Projector: &K8sGrantProjector{Dynamic: cfg.DynamicClient},
		WorkerID:  workerID,
	})
	river.AddWorker(workers, &EnrollmentWorker{
		Platform:      cfg.PlatformClient,
		CRManager:     &K8sCertificateRequestManager{Dynamic: cfg.DynamicClient},
		CSRProvider:   cfg.CSRProvider,
		ClusterIssuer: strings.TrimSpace(cfg.ClusterIssuer),
		WorkerID:      workerID,
	})
	river.AddWorker(workers, &AgentCleanupWorker{
		Platform: cfg.PlatformClient,
		Executor: cfg.CleanupExec,
		WorkerID: workerID,
	})

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema: QueueSchema,
		Queues: map[string]river.QueueConfig{
			QueueAccessControl: {MaxWorkers: maxWorkers},
		},
		Workers: workers,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("configure access intent River client: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("start access intent River workers: %w", err)
	}
	return &Runner{pool: pool, client: client}, nil
}

// Stop gracefully drains in-flight access intent jobs and closes the pool.
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var err error
	if r.client != nil {
		err = r.client.Stop(ctx)
	}
	if r.pool != nil {
		r.pool.Close()
	}
	return err
}
