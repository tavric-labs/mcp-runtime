package accessintentworker

import (
	"context"
	"errors"
	"strings"

	"github.com/riverqueue/river"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
)

// AgentCleanupPlatformClient is the Platform API surface used by AgentCleanupWorker.
type AgentCleanupPlatformClient interface {
	ClaimAgentCleanupIntentFence(ctx context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error)
	RecordAgentCleanupIntentReceipt(ctx context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error)
}

// AgentCleanupExecutor runs idempotent post-deactivation audit/cleanup follow-up
// without mutating authoritative session state (already revoked synchronously in Postgres).
type AgentCleanupExecutor func(ctx context.Context, intent apikit.AgentCleanupIntent) error

// AgentCleanupWorker reconciles post-deactivation agent cleanup follow-up jobs.
type AgentCleanupWorker struct {
	river.WorkerDefaults[ReconcileAgentCleanupJob]
	Platform AgentCleanupPlatformClient
	Executor AgentCleanupExecutor
	WorkerID string
}

func (w *AgentCleanupWorker) workerID() string {
	if strings.TrimSpace(w.WorkerID) != "" {
		return strings.TrimSpace(w.WorkerID)
	}
	return "runtime-access-agent-cleanup-worker"
}

func (w *AgentCleanupWorker) Work(ctx context.Context, job *river.Job[ReconcileAgentCleanupJob]) error {
	ctx = contextWithJobTrace(ctx, job.Args.RequestID, job.Args.Traceparent, job.Args.Tracestate)
	attempt := int(job.Attempt)

	claim, err := w.Platform.ClaimAgentCleanupIntentFence(ctx, job.Args.AgentID, w.workerID())
	if err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	epoch := claim.FenceEpoch

	if w.Executor != nil {
		if execErr := w.Executor(ctx, intent); execErr != nil {
			_, _ = w.Platform.RecordAgentCleanupIntentReceipt(ctx, intent.AgentID, apikit.AgentCleanupReceiptRequest{
				DesiredRevisionID: intent.DesiredRevisionID,
				FenceEpoch:        epoch,
				Outcome:           apikit.AccessIntentOutcomeFailed,
				Attempt:           attempt,
				WorkerID:          w.workerID(),
				LastErrorClass:    apikit.AccessIntentErrorTransient,
				LastError:         execErr.Error(),
			})
			return execErr
		}
	}

	if _, err := w.Platform.RecordAgentCleanupIntentReceipt(ctx, intent.AgentID, apikit.AgentCleanupReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.AccessIntentOutcomeCleaned,
		Attempt:           attempt,
		WorkerID:          w.workerID(),
	}); err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentConflict) {
			return nil
		}
		return err
	}
	return nil
}
