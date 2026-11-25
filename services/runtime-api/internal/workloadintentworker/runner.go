// Package workloadintentworker runs River v0.51.0 workers in runtime-api for
// durable MCPServer apply/retirement, raw managed Deployment/Service sync,
// staged image publication, and fixed-token imperative component restarts.
package workloadintentworker

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	// QueueSchema must match Platform API's workload_intent River schema.
	QueueSchema = "workload_intent_queue"
	// QueueWorkloads handles bounded server, deployment, and restart jobs.
	QueueWorkloads = "workloads"
	// QueueRegistryPublication isolates long-running image archive publications.
	QueueRegistryPublication = "registry_publication"

	KindReconcileServer           = "reconcile_server"
	KindReconcileDeployment       = "reconcile_deployment"
	KindReconcileImagePublication = "reconcile_image_publication"
	KindReconcileComponentRestart = "reconcile_component_restart"
)

// ReconcileServerJobArgs mirrors Platform API's reconcile_server River payload.
type ReconcileServerJobArgs struct {
	ServerID          string `json:"serverId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileServerJobArgs) Kind() string { return KindReconcileServer }

// ReconcileDeploymentJobArgs mirrors Platform API's reconcile_deployment payload.
type ReconcileDeploymentJobArgs struct {
	DeploymentID      string `json:"deploymentId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileDeploymentJobArgs) Kind() string { return KindReconcileDeployment }

// ReconcileImagePublicationJobArgs mirrors Platform API's reconcile_image_publication payload.
type ReconcileImagePublicationJobArgs struct {
	PublicationID     string `json:"publicationId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileImagePublicationJobArgs) Kind() string { return KindReconcileImagePublication }

// ReconcileComponentRestartJobArgs mirrors Platform API's reconcile_component_restart payload.
type ReconcileComponentRestartJobArgs struct {
	OperationID       string `json:"operationId"`
	DesiredRevisionID string `json:"desiredRevisionId"`
	RequestID         string `json:"requestId,omitempty"`
	Traceparent       string `json:"traceparent,omitempty"`
	Tracestate        string `json:"tracestate,omitempty"`
}

func (ReconcileComponentRestartJobArgs) Kind() string { return KindReconcileComponentRestart }

// RunnerConfig controls the River worker client and separate queue budgets.
type RunnerConfig struct {
	DSN                           string
	Schema                        string
	WorkerID                      string
	WorkloadsMaxWorkers           int
	RegistryPublicationMaxWorkers int
}

// DefaultRunnerConfig returns safe separate budgets for workloads and publications.
func DefaultRunnerConfig() RunnerConfig {
	return RunnerConfig{
		Schema:                        QueueSchema,
		WorkloadsMaxWorkers:           4,
		RegistryPublicationMaxWorkers: 2,
	}
}

// RiverQueues returns the dedicated River queue configuration map so image
// publication transfers cannot starve server/deployment/restart reconciliation.
func (c RunnerConfig) RiverQueues() map[string]river.QueueConfig {
	wMax := c.WorkloadsMaxWorkers
	if wMax <= 0 {
		wMax = 4
	}
	pMax := c.RegistryPublicationMaxWorkers
	if pMax <= 0 {
		pMax = 2
	}
	return map[string]river.QueueConfig{
		QueueWorkloads:           {MaxWorkers: wMax},
		QueueRegistryPublication: {MaxWorkers: pMax},
	}
}

// Runner owns the pgxpool and River worker client for workload intent queues.
type Runner struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
}

// Dependencies bundles the four typed workers registered in the River runner.
type Dependencies struct {
	Server      *ServerWorker
	Deployment  *DeploymentWorker
	Publication *ImagePublicationWorker
	Restart     *RestartWorker
}

// NewRunner creates a River v0.51.0 (riverpgxv5) worker runner on workload_intent_queue.
func NewRunner(ctx context.Context, cfg RunnerConfig, deps Dependencies) (*Runner, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, fmt.Errorf("workload intent worker DSN is required")
	}
	if cfg.Schema == "" {
		cfg.Schema = QueueSchema
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("connect workload intent worker pool: %w", err)
	}
	workers := river.NewWorkers()
	if deps.Server != nil {
		river.AddWorker(workers, deps.Server)
	}
	if deps.Deployment != nil {
		river.AddWorker(workers, deps.Deployment)
	}
	if deps.Publication != nil {
		river.AddWorker(workers, deps.Publication)
	}
	if deps.Restart != nil {
		river.AddWorker(workers, deps.Restart)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:  cfg.Schema,
		ID:      cfg.WorkerID,
		Queues:  cfg.RiverQueues(),
		Workers: workers,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create workload intent river client: %w", err)
	}
	return &Runner{pool: pool, client: client}, nil
}

// Start starts processing River queues asynchronously.
func (r *Runner) Start(ctx context.Context) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Start(ctx)
}

// Stop gracefully drains in-flight River jobs and closes the pool.
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
