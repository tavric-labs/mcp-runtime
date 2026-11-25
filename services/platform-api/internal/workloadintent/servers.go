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

const serverSelectSQL = `
SELECT
  i.server_id,
  i.namespace,
  i.name,
  i.team_id,
  i.desired_revision_id,
  i.lifecycle,
  i.spec_json,
  i.spec_digest,
  i.publication_id,
  i.retirement_checkpoints,
  i.target_cr_uid,
  i.updated_at,
  r.server_id IS NOT NULL,
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
  r.updated_at,
  o.server_id IS NOT NULL,
  COALESCE(o.observer_id, ''),
  COALESCE(o.observer_epoch, 0),
  COALESCE(o.observed_revision_id, ''),
  COALESCE(o.target_uid, ''),
  COALESCE(o.observed_generation, 0),
  COALESCE(o.health, ''),
  COALESCE(o.reason, ''),
  COALESCE(o.message, ''),
  COALESCE(o.conditions, '[]'::jsonb),
  o.last_transition_at,
  o.last_verified_at,
  o.updated_at
FROM workload_intent.server_intent i
LEFT JOIN workload_intent.server_receipt r ON r.server_id = i.server_id
LEFT JOIN workload_intent.server_observation o ON o.server_id = i.server_id`

func scanServerIntent(scanner rowScanner) (apikit.ServerIntent, error) {
	var (
		item            apikit.ServerIntent
		specJSON        []byte
		checkpointsJSON []byte
		hasReceipt      bool
		receipt         apikit.WorkloadExecutionReceipt
		receiptApplied  sql.NullTime
		receiptUpdated  sql.NullTime
		hasObs          bool
		obs             apikit.WorkloadStatusObservation
		obsCondsJSON    []byte
		obsTransitionAt sql.NullTime
		obsVerifiedAt   sql.NullTime
		obsUpdatedAt    sql.NullTime
	)
	if err := scanner.Scan(
		&item.ServerID,
		&item.Namespace,
		&item.Name,
		&item.TeamID,
		&item.DesiredRevisionID,
		&item.Lifecycle,
		&specJSON,
		&item.SpecDigest,
		&item.PublicationID,
		&checkpointsJSON,
		&item.TargetCRUID,
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
		&hasObs,
		&obs.ObserverID,
		&obs.ObserverEpoch,
		&obs.ObservedRevisionID,
		&obs.TargetUID,
		&obs.ObservedGeneration,
		&obs.Health,
		&obs.Reason,
		&obs.Message,
		&obsCondsJSON,
		&obsTransitionAt,
		&obsVerifiedAt,
		&obsUpdatedAt,
	); err != nil {
		return apikit.ServerIntent{}, err
	}
	if len(specJSON) > 0 {
		item.SpecJSON = json.RawMessage(specJSON)
	}
	if len(checkpointsJSON) > 0 {
		_ = json.Unmarshal(checkpointsJSON, &item.RetirementCheckpoints)
	}
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.Receipt = buildReceipt(hasReceipt, receipt, receiptApplied, receiptUpdated)
	item.Observation = buildObservation(hasObs, obs, obsCondsJSON, obsTransitionAt, obsVerifiedAt, obsUpdatedAt)
	return item, nil
}

// ListServerIntents returns all recorded MCPServer intents with receipts and observations.
func (w *Writer) ListServerIntents(ctx context.Context) ([]apikit.ServerIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, serverSelectSQL+` ORDER BY i.updated_at DESC, i.server_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.ServerIntent, 0)
	for rows.Next() {
		item, err := scanServerIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetServerIntent returns one MCPServer intent by server_id.
func (w *Writer) GetServerIntent(ctx context.Context, serverID string) (apikit.ServerIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.ServerIntent{}, false, ErrDisabled
	}
	return getServerIntentFrom(ctx, w.db, serverID)
}

func getServerIntentFrom(ctx context.Context, q queryRower, serverID string) (apikit.ServerIntent, bool, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return apikit.ServerIntent{}, false, nil
	}
	item, err := scanServerIntent(q.QueryRowContext(ctx, serverSelectSQL+` WHERE i.server_id = $1`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.ServerIntent{}, false, nil
	}
	if err != nil {
		return apikit.ServerIntent{}, false, err
	}
	return item, true, nil
}

// UpsertServerIntent atomically records desired Active MCPServer intent and
// enqueues reconcile_server on the workloads queue in one transaction.
func (w *Writer) UpsertServerIntent(ctx context.Context, req apikit.ServerIntentUpsertRequest) (apikit.ServerIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.ServerIntent{}, ErrDisabled
	}
	req.ServerID = strings.TrimSpace(req.ServerID)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.PublicationID = strings.TrimSpace(req.PublicationID)
	if req.ServerID == "" {
		req.ServerID = uuid.NewString()
	}
	if req.Namespace == "" || req.Name == "" {
		return apikit.ServerIntent{}, fmt.Errorf("%w: namespace and name are required", ErrInvalidInput)
	}
	canonicalSpec, err := normalizeSpecJSON(req.SpecJSON)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	digest := strings.TrimSpace(req.SpecDigest)
	if digest == "" {
		digest = digestBytes(canonicalSpec)
	}
	revisionID := uuid.NewString()
	emptyCheckpoints, _ := json.Marshal(apikit.ServerRetirementCheckpoints{})

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.server_intent (
  server_id, namespace, name, team_id, desired_revision_id, lifecycle,
  spec_json, spec_digest, publication_id, retirement_checkpoints, target_cr_uid, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10::jsonb, '', clock_timestamp())
ON CONFLICT (server_id) DO UPDATE SET
  namespace = EXCLUDED.namespace,
  name = EXCLUDED.name,
  team_id = EXCLUDED.team_id,
  desired_revision_id = EXCLUDED.desired_revision_id,
  lifecycle = EXCLUDED.lifecycle,
  spec_json = EXCLUDED.spec_json,
  spec_digest = EXCLUDED.spec_digest,
  publication_id = EXCLUDED.publication_id,
  retirement_checkpoints = EXCLUDED.retirement_checkpoints,
  updated_at = EXCLUDED.updated_at
RETURNING updated_at`,
		req.ServerID, req.Namespace, req.Name, req.TeamID, revisionID, apikit.ServerLifecycleActive,
		string(canonicalSpec), digest, req.PublicationID, string(emptyCheckpoints),
	).Scan(&updatedAt)
	if err != nil {
		return apikit.ServerIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileServerJobArgs{
		ServerID:          req.ServerID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueWorkloads}); err != nil {
		return apikit.ServerIntent{}, fmt.Errorf("enqueue server reconciliation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return apikit.ServerIntent{}, err
	}
	return apikit.ServerIntent{
		ServerID:          req.ServerID,
		Namespace:         req.Namespace,
		Name:              req.Name,
		TeamID:            req.TeamID,
		DesiredRevisionID: revisionID,
		Lifecycle:         apikit.ServerLifecycleActive,
		SpecJSON:          json.RawMessage(canonicalSpec),
		SpecDigest:        digest,
		PublicationID:     req.PublicationID,
		UpdatedAt:         updatedAt.UTC(),
	}, nil
}

// RetireServerIntent transitions a server intent to Retiring, captures TargetCRUID,
// and enqueues reconcile_server on workloads in one transaction.
func (w *Writer) RetireServerIntent(ctx context.Context, serverID string, req apikit.ServerIntentRetireRequest) (apikit.ServerIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.ServerIntent{}, ErrDisabled
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return apikit.ServerIntent{}, fmt.Errorf("%w: server_id is required", ErrInvalidInput)
	}
	targetUID := strings.TrimSpace(req.TargetCRUID)
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existingTargetUID, receiptTargetUID string
	err = tx.QueryRowContext(ctx, `
SELECT i.target_cr_uid, COALESCE(r.target_uid, '')
FROM workload_intent.server_intent i
LEFT JOIN workload_intent.server_receipt r ON r.server_id = i.server_id
WHERE i.server_id = $1
FOR UPDATE OF i`, serverID).Scan(&existingTargetUID, &receiptTargetUID)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	if targetUID == "" {
		if existingTargetUID != "" {
			targetUID = existingTargetUID
		} else {
			targetUID = receiptTargetUID
		}
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.server_intent
SET
  desired_revision_id = $2,
  lifecycle = $3,
  target_cr_uid = $4,
  updated_at = clock_timestamp()
WHERE server_id = $1`, serverID, revisionID, apikit.ServerLifecycleRetiring, targetUID); err != nil {
		return apikit.ServerIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileServerJobArgs{
		ServerID:          serverID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueWorkloads}); err != nil {
		return apikit.ServerIntent{}, fmt.Errorf("enqueue server retirement: %w", err)
	}

	item, ok, err := getServerIntentFrom(ctx, tx, serverID)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ServerIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ServerIntent{}, err
	}
	return item, nil
}

// ClaimServerFence bumps the per-server execution fence epoch and returns the
// authoritative desired server intent.
func (w *Writer) ClaimServerFence(ctx context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.ServerIntentFenceResponse{}, ErrDisabled
	}
	serverID = strings.TrimSpace(serverID)
	workerID = strings.TrimSpace(workerID)
	if serverID == "" || workerID == "" {
		return apikit.ServerIntentFenceResponse{}, fmt.Errorf("%w: server_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ServerIntentFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT server_id FROM workload_intent.server_intent WHERE server_id = $1 FOR SHARE`, serverID).Scan(&exists); err != nil {
		return apikit.ServerIntentFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.server_receipt (server_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (server_id) DO UPDATE SET
  fence_epoch = workload_intent.server_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, serverID, workerID).Scan(&epoch); err != nil {
		return apikit.ServerIntentFenceResponse{}, err
	}
	item, ok, err := getServerIntentFrom(ctx, tx, serverID)
	if err != nil {
		return apikit.ServerIntentFenceResponse{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ServerIntentFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ServerIntentFenceResponse{}, err
	}
	return apikit.ServerIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordServerRetirementCheckpoint durably persists ordered server retirement
// progress fenced by fence_epoch and desired_revision_id.
func (w *Writer) RecordServerRetirementCheckpoint(ctx context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error) {
	if w == nil || w.db == nil {
		return apikit.ServerIntent{}, ErrDisabled
	}
	serverID = strings.TrimSpace(serverID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.TargetCRUID = strings.TrimSpace(req.TargetCRUID)
	if serverID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" {
		return apikit.ServerIntent{}, fmt.Errorf("%w: server_id, desired_revision_id, positive fence_epoch, and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev   string
		lifecycle    string
		existingUID  string
		existingJSON []byte
		fenceEpoch   int64
	)
	err = tx.QueryRowContext(ctx, `
SELECT i.desired_revision_id, i.lifecycle, i.target_cr_uid, i.retirement_checkpoints, COALESCE(r.fence_epoch, 0)
FROM workload_intent.server_intent i
LEFT JOIN workload_intent.server_receipt r ON r.server_id = i.server_id
WHERE i.server_id = $1
FOR UPDATE OF i`, serverID).Scan(&desiredRev, &lifecycle, &existingUID, &existingJSON, &fenceEpoch)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	if fenceEpoch != req.FenceEpoch || desiredRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.ServerIntent{}, err
	}

	var merged apikit.ServerRetirementCheckpoints
	if len(existingJSON) > 0 {
		_ = json.Unmarshal(existingJSON, &merged)
	}
	// Checkpoints are monotonic: once a step is durably completed it stays true.
	merged.RetiringMarked = merged.RetiringMarked || req.Checkpoints.RetiringMarked
	merged.SessionsFenced = merged.SessionsFenced || req.Checkpoints.SessionsFenced
	merged.GrantsRetired = merged.GrantsRetired || req.Checkpoints.GrantsRetired
	merged.CredentialsCleaned = merged.CredentialsCleaned || req.Checkpoints.CredentialsCleaned
	merged.CRDeleted = merged.CRDeleted || req.Checkpoints.CRDeleted

	nextLifecycle := lifecycle
	if merged.RetiringMarked && merged.SessionsFenced && merged.GrantsRetired && merged.CredentialsCleaned && merged.CRDeleted {
		nextLifecycle = apikit.ServerLifecycleRetired
	}
	nextUID := existingUID
	if nextUID == "" && req.TargetCRUID != "" {
		nextUID = req.TargetCRUID
	}
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.server_intent
SET
  retirement_checkpoints = $2::jsonb,
  target_cr_uid = $3,
  lifecycle = $4,
  updated_at = clock_timestamp()
WHERE server_id = $1`, serverID, string(mergedJSON), nextUID, nextLifecycle); err != nil {
		return apikit.ServerIntent{}, err
	}
	item, ok, err := getServerIntentFrom(ctx, tx, serverID)
	if err != nil {
		return apikit.ServerIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ServerIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ServerIntent{}, err
	}
	return item, nil
}

// RecordServerReceipt records a fenced worker execution receipt for one server.
func (w *Writer) RecordServerReceipt(ctx context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadExecutionReceipt{}, ErrDisabled
	}
	serverID = strings.TrimSpace(serverID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.TargetUID = strings.TrimSpace(req.TargetUID)
	req.SpecDigest = strings.TrimSpace(req.SpecDigest)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if serverID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validOutcome(req.Outcome) {
		return apikit.WorkloadExecutionReceipt{}, fmt.Errorf("%w: server_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
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
		desiredRev        string
		currentFence      int64
		appliedRev        string
		existingTargetUID string
		existingGen       int64
		existingDigest    string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  r.fence_epoch,
  r.applied_revision_id,
  r.target_uid,
  r.target_generation,
  r.spec_digest
FROM workload_intent.server_intent i
JOIN workload_intent.server_receipt r ON r.server_id = i.server_id
WHERE i.server_id = $1
FOR UPDATE OF r`, serverID).Scan(&desiredRev, &currentFence, &appliedRev, &existingTargetUID, &existingGen, &existingDigest)
	if err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if currentFence != req.FenceEpoch {
		err = ErrStaleReceipt
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if req.Outcome != apikit.WorkloadIntentOutcomeSuperseded && desiredRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.WorkloadExecutionReceipt{}, err
	}

	nextApplied := appliedRev
	nextUID := existingTargetUID
	nextGen := existingGen
	nextDigest := existingDigest
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false

	switch req.Outcome {
	case apikit.WorkloadIntentOutcomeApplied, apikit.WorkloadIntentOutcomeCleaned:
		nextApplied = req.DesiredRevisionID
		if req.TargetUID != "" {
			nextUID = req.TargetUID
		}
		if req.TargetGeneration > 0 {
			nextGen = req.TargetGeneration
		}
		if req.SpecDigest != "" {
			nextDigest = req.SpecDigest
		}
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.WorkloadIntentOutcomeSuperseded:
		if desiredRev == req.DesiredRevisionID {
			err = ErrStaleReceipt
			return apikit.WorkloadExecutionReceipt{}, err
		}
		nextErrClass = ""
		nextErr = ""
	}

	var (
		out       apikit.WorkloadExecutionReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE workload_intent.server_receipt
SET
  applied_revision_id = $2,
  outcome = $3,
  target_uid = $4,
  target_generation = $5,
  spec_digest = $6,
  attempt = $7,
  worker_id = $8,
  last_error_class = $9,
  last_error = $10,
  applied_at = CASE WHEN $11 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE server_id = $1 AND fence_epoch = $12
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, target_uid, target_generation, spec_digest, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		serverID, nextApplied, req.Outcome, nextUID, nextGen, nextDigest, req.Attempt, req.WorkerID, nextErrClass, nextErr, markAppliedAt, req.FenceEpoch,
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
	if req.Outcome == apikit.WorkloadIntentOutcomeApplied && nextUID != "" {
		if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.server_intent SET target_cr_uid = $2 WHERE server_id = $1 AND target_cr_uid = ''`,
			serverID, nextUID); err != nil {
			return apikit.WorkloadExecutionReceipt{}, err
		}
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

// RecordServerObservation persists independent health observation without
// mutating desired intent, receipts, or River jobs.
func (w *Writer) RecordServerObservation(ctx context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadStatusObservation{}, ErrDisabled
	}
	serverID = strings.TrimSpace(serverID)
	req.ClusterKey = strings.TrimSpace(req.ClusterKey)
	if req.ClusterKey == "" {
		req.ClusterKey = defaultClusterKey
	}
	req.ObserverID = strings.TrimSpace(req.ObserverID)
	req.ObservedRevisionID = strings.TrimSpace(req.ObservedRevisionID)
	req.TargetUID = strings.TrimSpace(req.TargetUID)
	req.Health = strings.TrimSpace(req.Health)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Message = truncateSanitized(req.Message, maxStoredErrorLen)
	if serverID == "" || req.ObserverID == "" || req.ObserverEpoch <= 0 || !validHealth(req.Health) {
		return apikit.WorkloadStatusObservation{}, fmt.Errorf("%w: server_id, observer_id, positive observer_epoch, and valid health are required", ErrInvalidInput)
	}
	verifiedAt := req.VerifiedAt.UTC()
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	conds := req.Conditions
	if conds == nil {
		conds = []apikit.WorkloadIntentCondition{}
	}
	condsJSON, err := json.Marshal(conds)
	if err != nil {
		return apikit.WorkloadStatusObservation{}, err
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.WorkloadStatusObservation{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev string
		lifecycle  string
		appliedRev string
		receiptUID string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  i.lifecycle,
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.target_uid, '')
FROM workload_intent.server_intent i
LEFT JOIN workload_intent.server_receipt r ON r.server_id = i.server_id
WHERE i.server_id = $1
FOR SHARE OF i`, serverID).Scan(&desiredRev, &lifecycle, &appliedRev, &receiptUID)
	if err != nil {
		return apikit.WorkloadStatusObservation{}, err
	}

	var leaseEpoch int64
	if scanErr := tx.QueryRowContext(ctx, `SELECT observer_epoch FROM workload_intent.observer_lease WHERE cluster_key = $1`, req.ClusterKey).Scan(&leaseEpoch); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.WorkloadStatusObservation{}, err
	}
	if leaseEpoch > 0 && req.ObserverEpoch < leaseEpoch {
		err = ErrStaleObservation
		return apikit.WorkloadStatusObservation{}, err
	}

	var (
		existingEpoch      int64
		existingHealth     string
		existingReason     string
		existingTransition sql.NullTime
	)
	scanErr := tx.QueryRowContext(ctx, `
SELECT observer_epoch, health, reason, last_transition_at
FROM workload_intent.server_observation
WHERE server_id = $1
FOR UPDATE`, serverID).Scan(&existingEpoch, &existingHealth, &existingReason, &existingTransition)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.WorkloadStatusObservation{}, err
	}
	if existingEpoch > 0 && req.ObserverEpoch < existingEpoch {
		err = ErrStaleObservation
		return apikit.WorkloadStatusObservation{}, err
	}
	if req.ObservedRevisionID != "" && req.ObservedRevisionID != desiredRev && req.ObservedRevisionID != appliedRev {
		err = ErrStaleObservation
		return apikit.WorkloadStatusObservation{}, err
	}
	if req.Health == apikit.WorkloadIntentHealthReady {
		if lifecycle != apikit.ServerLifecycleActive || appliedRev == "" || appliedRev != desiredRev || req.ObservedRevisionID != appliedRev {
			err = ErrStaleObservation
			return apikit.WorkloadStatusObservation{}, err
		}
		if receiptUID != "" && req.TargetUID != receiptUID {
			err = ErrStaleObservation
			return apikit.WorkloadStatusObservation{}, err
		}
	}

	transitionAt := verifiedAt
	if existingTransition.Valid && existingHealth == req.Health && existingReason == req.Reason {
		transitionAt = existingTransition.Time.UTC()
	}

	var (
		out      apikit.WorkloadStatusObservation
		outConds []byte
	)
	err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.server_observation (
  server_id, observer_id, observer_epoch, observed_revision_id, target_uid, observed_generation,
  health, reason, message, conditions, last_transition_at, last_verified_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, clock_timestamp())
ON CONFLICT (server_id) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = EXCLUDED.observer_epoch,
  observed_revision_id = EXCLUDED.observed_revision_id,
  target_uid = EXCLUDED.target_uid,
  observed_generation = EXCLUDED.observed_generation,
  health = EXCLUDED.health,
  reason = EXCLUDED.reason,
  message = EXCLUDED.message,
  conditions = EXCLUDED.conditions,
  last_transition_at = EXCLUDED.last_transition_at,
  last_verified_at = EXCLUDED.last_verified_at,
  updated_at = EXCLUDED.updated_at
RETURNING observer_id, observer_epoch, observed_revision_id, target_uid, observed_generation, health, reason, message, conditions, last_transition_at, last_verified_at, updated_at`,
		serverID, req.ObserverID, req.ObserverEpoch, req.ObservedRevisionID, req.TargetUID, req.ObservedGeneration,
		req.Health, req.Reason, req.Message, string(condsJSON), transitionAt, verifiedAt,
	).Scan(
		&out.ObserverID, &out.ObserverEpoch, &out.ObservedRevisionID, &out.TargetUID,
		&out.ObservedGeneration, &out.Health, &out.Reason, &out.Message, &outConds,
		&out.LastTransitionAt, &out.LastVerifiedAt, &out.UpdatedAt,
	)
	if err != nil {
		return apikit.WorkloadStatusObservation{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.WorkloadStatusObservation{}, err
	}
	if len(outConds) > 0 {
		_ = json.Unmarshal(outConds, &out.Conditions)
	}
	out.LastTransitionAt = out.LastTransitionAt.UTC()
	out.LastVerifiedAt = out.LastVerifiedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}
