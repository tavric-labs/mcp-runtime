package accessintent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"mcp-runtime/pkg/apikit"
)

const agentCleanupSelectColumns = `
SELECT
  i.agent_id,
  i.team_id,
  i.desired_revision_id,
  i.deactivated_at,
  i.revoked_sessions_count,
  i.updated_at,
  r.agent_id IS NOT NULL,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.fence_holder, ''),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.outcome, ''),
  COALESCE(r.attempt, 0),
  COALESCE(r.worker_id, ''),
  COALESCE(r.last_error_class, ''),
  COALESCE(r.last_error, ''),
  r.applied_at,
  r.updated_at
FROM access_intent.agent_cleanup_intent i
LEFT JOIN access_intent.agent_cleanup_receipt r ON r.agent_id = i.agent_id`

func scanAgentCleanupIntent(scanner rowScanner) (apikit.AgentCleanupIntent, error) {
	var (
		item           apikit.AgentCleanupIntent
		hasReceipt     bool
		receipt        apikit.AgentCleanupReceipt
		receiptApplied sql.NullTime
		receiptUpdated sql.NullTime
	)
	if err := scanner.Scan(
		&item.AgentID,
		&item.TeamID,
		&item.DesiredRevisionID,
		&item.DeactivatedAt,
		&item.RevokedSessionsCount,
		&item.UpdatedAt,
		&hasReceipt,
		&receipt.FenceEpoch,
		&receipt.FenceHolder,
		&receipt.AppliedRevisionID,
		&receipt.Outcome,
		&receipt.Attempt,
		&receipt.WorkerID,
		&receipt.LastErrorClass,
		&receipt.LastError,
		&receiptApplied,
		&receiptUpdated,
	); err != nil {
		return apikit.AgentCleanupIntent{}, err
	}
	item.DeactivatedAt = item.DeactivatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if hasReceipt {
		if receiptApplied.Valid {
			t := receiptApplied.Time.UTC()
			receipt.AppliedAt = &t
		}
		if receiptUpdated.Valid {
			receipt.UpdatedAt = receiptUpdated.Time.UTC()
		}
		item.Receipt = &receipt
	}
	return item, nil
}

// RecordAgentDeactivationTx records post-deactivation follow-up cleanup intent
// and enqueues reconcile_agent_cleanup inside the caller's existing atomic
// agent deactivation + session revocation transaction. Authoritative session
// revocation MUST have already executed in tx before calling this helper.
func (w *Writer) RecordAgentDeactivationTx(ctx context.Context, tx *sql.Tx, agentID, teamID string, deactivatedAt time.Time, revokedSessionsCount int64) error {
	if w == nil || w.queue == nil {
		return ErrDisabled
	}
	agentID = strings.TrimSpace(agentID)
	teamID = strings.TrimSpace(teamID)
	if agentID == "" || teamID == "" {
		return fmt.Errorf("%w: agent_id and team_id are required", ErrInvalidInput)
	}
	if deactivatedAt.IsZero() {
		deactivatedAt = time.Now().UTC()
	}
	revisionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO access_intent.agent_cleanup_intent (
  agent_id, team_id, desired_revision_id, deactivated_at, revoked_sessions_count, updated_at
) VALUES ($1, $2, $3, $4, $5, clock_timestamp())
ON CONFLICT (agent_id) DO UPDATE SET
  team_id = EXCLUDED.team_id,
  desired_revision_id = EXCLUDED.desired_revision_id,
  deactivated_at = EXCLUDED.deactivated_at,
  revoked_sessions_count = EXCLUDED.revoked_sessions_count,
  updated_at = EXCLUDED.updated_at`,
		agentID, teamID, revisionID, deactivatedAt.UTC(), revokedSessionsCount,
	); err != nil {
		return fmt.Errorf("record agent cleanup intent: %w", err)
	}
	if _, err := w.queue.InsertTx(ctx, tx, newReconcileAgentCleanupJob(ctx, agentID, revisionID), &river.InsertOpts{Queue: QueueAccessControl}); err != nil {
		return fmt.Errorf("enqueue agent cleanup reconciliation: %w", err)
	}
	return nil
}

// GetAgentCleanupIntent returns the durable post-deactivation cleanup intent for one agent.
func (w *Writer) GetAgentCleanupIntent(ctx context.Context, agentID string) (apikit.AgentCleanupIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.AgentCleanupIntent{}, false, ErrDisabled
	}
	return getAgentCleanupIntentFrom(ctx, w.db, agentID)
}

func getAgentCleanupIntentFrom(ctx context.Context, q queryRower, agentID string) (apikit.AgentCleanupIntent, bool, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return apikit.AgentCleanupIntent{}, false, nil
	}
	item, err := scanAgentCleanupIntent(q.QueryRowContext(ctx, agentCleanupSelectColumns+` WHERE i.agent_id = $1`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.AgentCleanupIntent{}, false, nil
	}
	if err != nil {
		return apikit.AgentCleanupIntent{}, false, err
	}
	return item, true, nil
}

// ClaimAgentCleanupFence increments the per-agent execution fence and returns
// the current post-deactivation cleanup intent.
func (w *Writer) ClaimAgentCleanupFence(ctx context.Context, agentID, workerID string) (apikit.AgentCleanupFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.AgentCleanupFenceResponse{}, ErrDisabled
	}
	agentID = strings.TrimSpace(agentID)
	workerID = strings.TrimSpace(workerID)
	if agentID == "" || workerID == "" {
		return apikit.AgentCleanupFenceResponse{}, fmt.Errorf("%w: agent_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.AgentCleanupFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM access_intent.agent_cleanup_intent WHERE agent_id = $1 FOR SHARE`, agentID).Scan(&exists); err != nil {
		return apikit.AgentCleanupFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO access_intent.agent_cleanup_receipt (agent_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (agent_id) DO UPDATE SET
  fence_epoch = access_intent.agent_cleanup_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, agentID, workerID).Scan(&epoch); err != nil {
		return apikit.AgentCleanupFenceResponse{}, err
	}
	item, found, getErr := getAgentCleanupIntentFrom(ctx, tx, agentID)
	if getErr != nil {
		err = getErr
		return apikit.AgentCleanupFenceResponse{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.AgentCleanupFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.AgentCleanupFenceResponse{}, err
	}
	return apikit.AgentCleanupFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordAgentCleanupReceipt persists a fenced post-deactivation cleanup receipt.
func (w *Writer) RecordAgentCleanupReceipt(ctx context.Context, agentID string, req apikit.AgentCleanupReceiptRequest) (apikit.AgentCleanupReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.AgentCleanupReceipt{}, ErrDisabled
	}
	agentID = strings.TrimSpace(agentID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if agentID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validAgentCleanupOutcome(req.Outcome) {
		return apikit.AgentCleanupReceipt{}, fmt.Errorf("%w: agent_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.AgentCleanupReceipt{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		currentRev   string
		currentEpoch int64
		appliedRev   string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.applied_revision_id, '')
FROM access_intent.agent_cleanup_intent i
LEFT JOIN access_intent.agent_cleanup_receipt r ON r.agent_id = i.agent_id
WHERE i.agent_id = $1
FOR UPDATE OF i`, agentID).Scan(&currentRev, &currentEpoch, &appliedRev)
	if err != nil {
		return apikit.AgentCleanupReceipt{}, err
	}
	if currentEpoch != req.FenceEpoch || currentRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.AgentCleanupReceipt{}, err
	}

	nextAppliedRev := appliedRev
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false
	if req.Outcome == apikit.AccessIntentOutcomeCleaned {
		nextAppliedRev = req.DesiredRevisionID
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	}

	var (
		out       apikit.AgentCleanupReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE access_intent.agent_cleanup_receipt
SET
  applied_revision_id = $2,
  outcome = $3,
  attempt = $4,
  worker_id = $5,
  last_error_class = $6,
  last_error = $7,
  applied_at = CASE WHEN $8 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE agent_id = $1 AND fence_epoch = $9
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		agentID,
		nextAppliedRev,
		req.Outcome,
		req.Attempt,
		req.WorkerID,
		nextErrClass,
		nextErr,
		markAppliedAt,
		req.FenceEpoch,
	).Scan(
		&out.FenceEpoch,
		&out.FenceHolder,
		&out.AppliedRevisionID,
		&out.Outcome,
		&out.Attempt,
		&out.WorkerID,
		&out.LastErrorClass,
		&out.LastError,
		&appliedAt,
		&out.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrStaleReceipt
		return apikit.AgentCleanupReceipt{}, err
	}
	if err != nil {
		return apikit.AgentCleanupReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.AgentCleanupReceipt{}, err
	}
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		out.AppliedAt = &t
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func validAgentCleanupOutcome(v string) bool {
	switch v {
	case apikit.AccessIntentOutcomeCleaned,
		apikit.AccessIntentOutcomeSuperseded,
		apikit.AccessIntentOutcomeFailed,
		apikit.AccessIntentOutcomeTerminal:
		return true
	default:
		return false
	}
}
