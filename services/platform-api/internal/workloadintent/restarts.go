package workloadintent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"mcp-runtime/pkg/apikit"
)

const restartSelectSQL = `
SELECT
  i.operation_id,
  i.requested_by,
  i.target_components,
  i.restarted_at,
  i.component_checkpoints,
  i.desired_revision_id,
  i.status,
  i.updated_at,
  r.operation_id IS NOT NULL,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.fence_holder, ''),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.outcome, ''),
  COALESCE(r.target_uid, ''),
  COALESCE(r.target_generation, 0),
  COALESCE(r.spec_digest, ''),
  COALESCE(r.attempt, 0),
  COALESCE(r.worker_id, ''),
  COALESCE(r.last_error_class, ''),
  COALESCE(r.last_error, ''),
  r.applied_at,
  r.updated_at
FROM workload_intent.restart_operation_intent i
LEFT JOIN workload_intent.restart_operation_receipt r ON r.operation_id = i.operation_id`

func scanRestartIntent(scanner rowScanner) (apikit.RestartOperationIntent, error) {
	var (
		item            apikit.RestartOperationIntent
		componentsJSON  []byte
		checkpointsJSON []byte
		hasReceipt      bool
		receipt         apikit.WorkloadExecutionReceipt
		receiptApplied  sql.NullTime
		receiptUpdated  sql.NullTime
	)
	if err := scanner.Scan(
		&item.OperationID,
		&item.RequestedBy,
		&componentsJSON,
		&item.RestartedAt,
		&checkpointsJSON,
		&item.DesiredRevisionID,
		&item.Status,
		&item.UpdatedAt,
		&hasReceipt,
		&receipt.FenceEpoch,
		&receipt.FenceHolder,
		&receipt.AppliedRevisionID,
		&receipt.Outcome,
		&receipt.TargetUID,
		&receipt.TargetGeneration,
		&receipt.SpecDigest,
		&receipt.Attempt,
		&receipt.WorkerID,
		&receipt.LastErrorClass,
		&receipt.LastError,
		&receiptApplied,
		&receiptUpdated,
	); err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	if len(componentsJSON) > 0 {
		_ = json.Unmarshal(componentsJSON, &item.TargetComponents)
	}
	if len(checkpointsJSON) > 0 {
		_ = json.Unmarshal(checkpointsJSON, &item.ComponentCheckpoints)
	}
	if item.ComponentCheckpoints == nil {
		item.ComponentCheckpoints = map[string]apikit.ComponentRestartCheckpoint{}
	}
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.Receipt = buildReceipt(hasReceipt, receipt, receiptApplied, receiptUpdated)
	return item, nil
}

// ListRestartOperationIntents returns all recorded imperative restart operations.
func (w *Writer) ListRestartOperationIntents(ctx context.Context) ([]apikit.RestartOperationIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, restartSelectSQL+` ORDER BY i.updated_at DESC, i.operation_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.RestartOperationIntent, 0)
	for rows.Next() {
		item, err := scanRestartIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetRestartOperationIntent returns one restart operation by operation_id.
func (w *Writer) GetRestartOperationIntent(ctx context.Context, operationID string) (apikit.RestartOperationIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.RestartOperationIntent{}, false, ErrDisabled
	}
	return getRestartIntentFrom(ctx, w.db, operationID)
}

func getRestartIntentFrom(ctx context.Context, q queryRower, operationID string) (apikit.RestartOperationIntent, bool, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return apikit.RestartOperationIntent{}, false, nil
	}
	item, err := scanRestartIntent(q.QueryRowContext(ctx, restartSelectSQL+` WHERE i.operation_id = $1`, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.RestartOperationIntent{}, false, nil
	}
	if err != nil {
		return apikit.RestartOperationIntent{}, false, err
	}
	return item, true, nil
}

// CreateRestartOperation atomically persists a FIXED acceptance-time
// RestartedAt RFC3339 token, target components, and enqueues
// reconcile_component_restart on the workloads queue in one transaction.
func (w *Writer) CreateRestartOperation(ctx context.Context, req apikit.RestartOperationCreateRequest) (apikit.RestartOperationIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.RestartOperationIntent{}, ErrDisabled
	}
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.RequestedBy = strings.TrimSpace(req.RequestedBy)
	if req.OperationID == "" {
		req.OperationID = uuid.NewString()
	}
	if req.RequestedBy == "" {
		return apikit.RestartOperationIntent{}, fmt.Errorf("%w: requested_by is required", ErrInvalidInput)
	}
	components := dedupeOrderedStrings(req.TargetComponents)
	if len(components) == 0 {
		return apikit.RestartOperationIntent{}, fmt.Errorf("%w: target_components must not be empty", ErrInvalidInput)
	}
	restartedAt := strings.TrimSpace(req.RestartedAt)
	if restartedAt == "" {
		restartedAt = time.Now().UTC().Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, restartedAt); err != nil {
		return apikit.RestartOperationIntent{}, fmt.Errorf("%w: restarted_at must be RFC3339", ErrInvalidInput)
	}
	revisionID := uuid.NewString()
	checkpoints := make(map[string]apikit.ComponentRestartCheckpoint, len(components))
	for _, comp := range components {
		checkpoints[comp] = apikit.ComponentRestartCheckpoint{Component: comp, Completed: false}
	}
	componentsJSON, err := json.Marshal(components)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	checkpointsJSON, err := json.Marshal(checkpoints)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.restart_operation_intent (
  operation_id, requested_by, target_components, restarted_at,
  component_checkpoints, desired_revision_id, status, updated_at
) VALUES ($1, $2, $3::jsonb, $4, $5::jsonb, $6, $7, clock_timestamp())
RETURNING updated_at`,
		req.OperationID, req.RequestedBy, string(componentsJSON), restartedAt,
		string(checkpointsJSON), revisionID, apikit.RestartStatusQueued,
	).Scan(&updatedAt)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileComponentRestartJobArgs{
		OperationID:       req.OperationID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueWorkloads}); err != nil {
		return apikit.RestartOperationIntent{}, fmt.Errorf("enqueue component restart: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	return apikit.RestartOperationIntent{
		OperationID:          req.OperationID,
		RequestedBy:          req.RequestedBy,
		TargetComponents:     components,
		RestartedAt:          restartedAt,
		ComponentCheckpoints: checkpoints,
		DesiredRevisionID:    revisionID,
		Status:               apikit.RestartStatusQueued,
		UpdatedAt:            updatedAt.UTC(),
	}, nil
}

// ClaimRestartOperationFence bumps the execution fence and marks Queued -> InProgress.
func (w *Writer) ClaimRestartOperationFence(ctx context.Context, operationID, workerID string) (apikit.RestartOperationFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.RestartOperationFenceResponse{}, ErrDisabled
	}
	operationID = strings.TrimSpace(operationID)
	workerID = strings.TrimSpace(workerID)
	if operationID == "" || workerID == "" {
		return apikit.RestartOperationFenceResponse{}, fmt.Errorf("%w: operation_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.RestartOperationFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM workload_intent.restart_operation_intent WHERE operation_id = $1 FOR UPDATE`, operationID).Scan(&status); err != nil {
		return apikit.RestartOperationFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.restart_operation_receipt (operation_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (operation_id) DO UPDATE SET
  fence_epoch = workload_intent.restart_operation_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, operationID, workerID).Scan(&epoch); err != nil {
		return apikit.RestartOperationFenceResponse{}, err
	}
	if status == apikit.RestartStatusQueued {
		if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.restart_operation_intent SET status = $2, updated_at = clock_timestamp() WHERE operation_id = $1`,
			operationID, apikit.RestartStatusInProgress); err != nil {
			return apikit.RestartOperationFenceResponse{}, err
		}
	}

	item, ok, err := getRestartIntentFrom(ctx, tx, operationID)
	if err != nil {
		return apikit.RestartOperationFenceResponse{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.RestartOperationFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.RestartOperationFenceResponse{}, err
	}
	return apikit.RestartOperationFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordRestartComponentCheckpoint durably records completion for one target
// component under the active execution fence without modifying RestartedAt.
func (w *Writer) RecordRestartComponentCheckpoint(ctx context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error) {
	if w == nil || w.db == nil {
		return apikit.RestartOperationIntent{}, ErrDisabled
	}
	operationID = strings.TrimSpace(operationID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.Checkpoint.Component = strings.TrimSpace(req.Checkpoint.Component)
	req.Checkpoint.TargetUID = strings.TrimSpace(req.Checkpoint.TargetUID)
	req.Checkpoint.Error = truncateSanitized(req.Checkpoint.Error, maxStoredErrorLen)
	if operationID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || req.Checkpoint.Component == "" {
		return apikit.RestartOperationIntent{}, fmt.Errorf("%w: operation_id, desired_revision_id, positive fence_epoch, worker_id, and component are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev   string
		existingJSON []byte
		fenceEpoch   int64
	)
	err = tx.QueryRowContext(ctx, `
SELECT i.desired_revision_id, i.component_checkpoints, COALESCE(r.fence_epoch, 0)
FROM workload_intent.restart_operation_intent i
LEFT JOIN workload_intent.restart_operation_receipt r ON r.operation_id = i.operation_id
WHERE i.operation_id = $1
FOR UPDATE OF i`, operationID).Scan(&desiredRev, &existingJSON, &fenceEpoch)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	if fenceEpoch != req.FenceEpoch || desiredRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.RestartOperationIntent{}, err
	}

	checkpoints := map[string]apikit.ComponentRestartCheckpoint{}
	if len(existingJSON) > 0 {
		_ = json.Unmarshal(existingJSON, &checkpoints)
	}
	prev := checkpoints[req.Checkpoint.Component]
	next := req.Checkpoint
	// Preserve completed=true once durably recorded.
	if prev.Completed {
		next.Completed = true
		if next.PatchedAt == nil {
			next.PatchedAt = prev.PatchedAt
		}
		if next.TargetUID == "" {
			next.TargetUID = prev.TargetUID
		}
	}
	checkpoints[req.Checkpoint.Component] = next

	updatedJSON, err := json.Marshal(checkpoints)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.restart_operation_intent
SET component_checkpoints = $2::jsonb, updated_at = clock_timestamp()
WHERE operation_id = $1`, operationID, string(updatedJSON)); err != nil {
		return apikit.RestartOperationIntent{}, err
	}

	item, ok, err := getRestartIntentFrom(ctx, tx, operationID)
	if err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.RestartOperationIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.RestartOperationIntent{}, err
	}
	return item, nil
}

// RecordRestartOperationReceipt records a fenced final receipt for a restart operation.
func (w *Writer) RecordRestartOperationReceipt(ctx context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadExecutionReceipt{}, ErrDisabled
	}
	operationID = strings.TrimSpace(operationID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.Status = strings.TrimSpace(req.Status)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if operationID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validOutcome(req.Outcome) {
		return apikit.WorkloadExecutionReceipt{}, fmt.Errorf("%w: operation_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev   string
		currentFence int64
		appliedRev   string
		curStatus    string
	)
	err = tx.QueryRowContext(ctx, `
SELECT i.desired_revision_id, r.fence_epoch, r.applied_revision_id, i.status
FROM workload_intent.restart_operation_intent i
JOIN workload_intent.restart_operation_receipt r ON r.operation_id = i.operation_id
WHERE i.operation_id = $1
FOR UPDATE OF i, r`, operationID).Scan(&desiredRev, &currentFence, &appliedRev, &curStatus)
	if err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if currentFence != req.FenceEpoch || desiredRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.WorkloadExecutionReceipt{}, err
	}

	nextApplied := appliedRev
	nextStatus := curStatus
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false

	switch req.Outcome {
	case apikit.WorkloadIntentOutcomeApplied:
		nextApplied = req.DesiredRevisionID
		nextStatus = apikit.RestartStatusCompleted
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.WorkloadIntentOutcomeFailed, apikit.WorkloadIntentOutcomeTerminal:
		if req.Status != "" {
			nextStatus = req.Status
		} else {
			nextStatus = apikit.RestartStatusFailed
		}
	default:
		if req.Status != "" {
			nextStatus = req.Status
		}
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.restart_operation_intent SET status = $2, updated_at = clock_timestamp() WHERE operation_id = $1`,
		operationID, nextStatus); err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}

	var (
		out       apikit.WorkloadExecutionReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE workload_intent.restart_operation_receipt
SET
  applied_revision_id = $2,
  outcome = $3,
  attempt = $4,
  worker_id = $5,
  last_error_class = $6,
  last_error = $7,
  applied_at = CASE WHEN $8 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE operation_id = $1 AND fence_epoch = $9
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, target_uid, target_generation, spec_digest, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		operationID, nextApplied, req.Outcome, req.Attempt, req.WorkerID, nextErrClass, nextErr, markAppliedAt, req.FenceEpoch,
	).Scan(
		&out.FenceEpoch, &out.FenceHolder, &out.AppliedRevisionID, &out.Outcome,
		&out.TargetUID, &out.TargetGeneration, &out.SpecDigest, &out.Attempt,
		&out.WorkerID, &out.LastErrorClass, &out.LastError, &appliedAt, &out.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrStaleReceipt
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		out.AppliedAt = &t
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func dedupeOrderedStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
