package accessintent

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

const grantSelectColumns = `
SELECT
  i.grant_id,
  i.namespace,
  i.name,
  i.team_id,
  i.server_name,
  i.server_uid,
  i.subject_json,
  i.spec_json,
  i.desired_revision_id,
  i.state,
  i.revocation_committed_at,
  i.created_at,
  i.updated_at,
  r.grant_id IS NOT NULL,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.fence_holder, ''),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.target_uid, ''),
  COALESCE(r.target_generation, 0),
  COALESCE(r.spec_digest, ''),
  COALESCE(r.outcome, ''),
  COALESCE(r.attempt, 0),
  COALESCE(r.worker_id, ''),
  COALESCE(r.last_error_class, ''),
  COALESCE(r.last_error, ''),
  r.applied_at,
  r.updated_at,
  o.grant_id IS NOT NULL,
  COALESCE(o.observer_id, ''),
  COALESCE(o.observer_epoch, 0),
  COALESCE(o.observed_revision_id, ''),
  COALESCE(o.target_uid, ''),
  COALESCE(o.health, 'Unknown'),
  COALESCE(o.reason, ''),
  COALESCE(o.message, ''),
  COALESCE(o.propagation_status, 'Unknown'),
  COALESCE(o.serving_replicas, 0),
  COALESCE(o.acknowledged_replicas, 0),
  COALESCE(o.gateway_evidence, '[]'::jsonb),
  o.last_transition_at,
  o.last_verified_at,
  o.updated_at
FROM access_intent.grant_intent i
LEFT JOIN access_intent.grant_receipt r ON r.grant_id = i.grant_id
LEFT JOIN access_intent.grant_observation o ON o.grant_id = i.grant_id`

func scanGrantIntent(scanner rowScanner) (apikit.GrantIntent, error) {
	var (
		item            apikit.GrantIntent
		subjectJSON     []byte
		specJSON        []byte
		revokedAt       sql.NullTime
		hasReceipt      bool
		receipt         apikit.GrantReceipt
		receiptApplied  sql.NullTime
		receiptUpdated  sql.NullTime
		hasObs          bool
		obs             apikit.GrantObservation
		obsEvidenceJSON []byte
		obsTransitionAt sql.NullTime
		obsVerifiedAt   sql.NullTime
		obsUpdatedAt    sql.NullTime
	)
	if err := scanner.Scan(
		&item.GrantID,
		&item.Namespace,
		&item.Name,
		&item.TeamID,
		&item.ServerName,
		&item.ServerUID,
		&subjectJSON,
		&specJSON,
		&item.DesiredRevisionID,
		&item.State,
		&revokedAt,
		&item.CreatedAt,
		&item.UpdatedAt,
		&hasReceipt,
		&receipt.FenceEpoch,
		&receipt.FenceHolder,
		&receipt.AppliedRevisionID,
		&receipt.TargetUID,
		&receipt.TargetGeneration,
		&receipt.SpecDigest,
		&receipt.Outcome,
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
		&obs.Health,
		&obs.Reason,
		&obs.Message,
		&obs.PropagationStatus,
		&obs.ServingReplicas,
		&obs.AcknowledgedReplicas,
		&obsEvidenceJSON,
		&obsTransitionAt,
		&obsVerifiedAt,
		&obsUpdatedAt,
	); err != nil {
		return apikit.GrantIntent{}, err
	}
	if len(subjectJSON) > 0 {
		_ = json.Unmarshal(subjectJSON, &item.Subject)
	}
	if len(specJSON) > 0 {
		item.SpecJSON = append(json.RawMessage(nil), specJSON...)
	}
	if revokedAt.Valid {
		t := revokedAt.Time.UTC()
		item.RevocationCommittedAt = &t
	}
	item.CreatedAt = item.CreatedAt.UTC()
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
	if hasObs {
		if len(obsEvidenceJSON) > 0 {
			_ = json.Unmarshal(obsEvidenceJSON, &obs.GatewayEvidence)
		}
		if obsTransitionAt.Valid {
			obs.LastTransitionAt = obsTransitionAt.Time.UTC()
		}
		if obsVerifiedAt.Valid {
			obs.LastVerifiedAt = obsVerifiedAt.Time.UTC()
		}
		if obsUpdatedAt.Valid {
			obs.UpdatedAt = obsUpdatedAt.Time.UTC()
		}
		item.Observation = &obs
	}
	item.GatewayPropagation = apikit.ComputeGrantGatewayPropagation(
		item.State,
		item.DesiredRevisionID,
		item.RevocationCommittedAt,
		item.Receipt,
		item.Observation,
	)
	return item, nil
}

// UpsertGrantIntent atomically records desired grant intent and schedules a
// reconcile_grant River job in one PostgreSQL transaction.
func (w *Writer) UpsertGrantIntent(ctx context.Context, req apikit.GrantIntentUpsertRequest) (apikit.GrantIntent, error) {
	if w == nil || w.db == nil || w.queue == nil {
		return apikit.GrantIntent{}, ErrDisabled
	}
	req.GrantID = strings.TrimSpace(req.GrantID)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.ServerName = strings.TrimSpace(req.ServerName)
	req.ServerUID = strings.TrimSpace(req.ServerUID)
	if req.Namespace == "" || req.Name == "" || req.TeamID == "" || req.ServerName == "" || req.ServerUID == "" {
		return apikit.GrantIntent{}, fmt.Errorf("%w: namespace, name, team_id, server_name, and server_uid are required", ErrInvalidInput)
	}
	specBytes := req.SpecJSON
	if len(specBytes) == 0 {
		specBytes = json.RawMessage(`{}`)
	}
	if !json.Valid(specBytes) {
		return apikit.GrantIntent{}, fmt.Errorf("%w: spec_json must be valid JSON", ErrInvalidInput)
	}
	subjectBytes, err := json.Marshal(req.Subject)
	if err != nil {
		return apikit.GrantIntent{}, err
	}
	grantID := req.GrantID
	if grantID == "" {
		grantID = uuid.NewString()
	}
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.GrantIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existingState string
	scanErr := tx.QueryRowContext(ctx, `SELECT state FROM access_intent.grant_intent WHERE grant_id = $1 FOR UPDATE`, grantID).Scan(&existingState)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.GrantIntent{}, err
	}
	if existingState == apikit.GrantIntentStateDeleted {
		err = fmt.Errorf("%w: deleted grant incarnation cannot be reactivated", ErrStaleReceipt)
		return apikit.GrantIntent{}, err
	}

	if _, err = tx.ExecContext(ctx, `
INSERT INTO access_intent.grant_intent (
  grant_id, namespace, name, team_id, server_name, server_uid,
  subject_json, spec_json, desired_revision_id, state,
  revocation_committed_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10, NULL, clock_timestamp(), clock_timestamp())
ON CONFLICT (grant_id) DO UPDATE SET
  namespace = EXCLUDED.namespace,
  name = EXCLUDED.name,
  team_id = EXCLUDED.team_id,
  server_name = EXCLUDED.server_name,
  server_uid = EXCLUDED.server_uid,
  subject_json = EXCLUDED.subject_json,
  spec_json = EXCLUDED.spec_json,
  desired_revision_id = EXCLUDED.desired_revision_id,
  state = EXCLUDED.state,
  revocation_committed_at = NULL,
  updated_at = EXCLUDED.updated_at`,
		grantID, req.Namespace, req.Name, req.TeamID, req.ServerName, req.ServerUID,
		string(subjectBytes), string(specBytes), revisionID, apikit.GrantIntentStateActive,
	); err != nil {
		return apikit.GrantIntent{}, fmt.Errorf("upsert grant intent: %w", err)
	}

	if _, err = w.queue.InsertTx(ctx, tx, newReconcileGrantJob(ctx, grantID, revisionID), &river.InsertOpts{Queue: QueueAccessControl}); err != nil {
		return apikit.GrantIntent{}, fmt.Errorf("enqueue grant reconciliation: %w", err)
	}
	item, found, getErr := getGrantIntentFrom(ctx, tx, grantID)
	if getErr != nil {
		err = getErr
		return apikit.GrantIntent{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.GrantIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.GrantIntent{}, err
	}
	return item, nil
}

// RevokeGrantIntent commits authoritative grant revocation or deletion
// synchronously in PostgreSQL FIRST and enqueues Kubernetes cleanup/projection
// in the same transaction.
func (w *Writer) RevokeGrantIntent(ctx context.Context, grantID string, deleteGrant bool) (apikit.GrantIntent, error) {
	if w == nil || w.db == nil || w.queue == nil {
		return apikit.GrantIntent{}, ErrDisabled
	}
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return apikit.GrantIntent{}, fmt.Errorf("%w: grant_id is required", ErrInvalidInput)
	}
	targetState := apikit.GrantIntentStateRevoked
	if deleteGrant {
		targetState = apikit.GrantIntentStateDeleted
	}
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.GrantIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var currentState string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM access_intent.grant_intent WHERE grant_id = $1 FOR UPDATE`, grantID).Scan(&currentState); err != nil {
		return apikit.GrantIntent{}, err
	}
	if currentState == apikit.GrantIntentStateDeleted && !deleteGrant {
		// Never downgrade a Deleted grant back to Revoked.
		targetState = apikit.GrantIntentStateDeleted
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE access_intent.grant_intent
SET
  state = $2,
  desired_revision_id = $3,
  revocation_committed_at = COALESCE(revocation_committed_at, clock_timestamp()),
  updated_at = clock_timestamp()
WHERE grant_id = $1`, grantID, targetState, revisionID); err != nil {
		return apikit.GrantIntent{}, fmt.Errorf("commit grant revocation intent: %w", err)
	}

	if _, err = w.queue.InsertTx(ctx, tx, newReconcileGrantJob(ctx, grantID, revisionID), &river.InsertOpts{Queue: QueueAccessControl}); err != nil {
		return apikit.GrantIntent{}, fmt.Errorf("enqueue grant revocation reconciliation: %w", err)
	}

	item, found, getErr := getGrantIntentFrom(ctx, tx, grantID)
	if getErr != nil {
		err = getErr
		return apikit.GrantIntent{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.GrantIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.GrantIntent{}, err
	}
	return item, nil
}

// ListGrantIntents returns all durable grant intents with receipts,
// independent observations, and separated gateway propagation milestones.
func (w *Writer) ListGrantIntents(ctx context.Context) ([]apikit.GrantIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, grantSelectColumns+` ORDER BY i.updated_at DESC, i.grant_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.GrantIntent, 0)
	for rows.Next() {
		item, err := scanGrantIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetGrantIntent returns one durable grant incarnation.
func (w *Writer) GetGrantIntent(ctx context.Context, grantID string) (apikit.GrantIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.GrantIntent{}, false, ErrDisabled
	}
	return getGrantIntentFrom(ctx, w.db, grantID)
}

func getGrantIntentFrom(ctx context.Context, q queryRower, grantID string) (apikit.GrantIntent, bool, error) {
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return apikit.GrantIntent{}, false, nil
	}
	item, err := scanGrantIntent(q.QueryRowContext(ctx, grantSelectColumns+` WHERE i.grant_id = $1`, grantID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.GrantIntent{}, false, nil
	}
	if err != nil {
		return apikit.GrantIntent{}, false, err
	}
	return item, true, nil
}

// ClaimGrantFence atomically increments the per-grant worker execution fence
// and returns the authoritative current desired intent.
func (w *Writer) ClaimGrantFence(ctx context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.GrantIntentFenceResponse{}, ErrDisabled
	}
	grantID = strings.TrimSpace(grantID)
	workerID = strings.TrimSpace(workerID)
	if grantID == "" || workerID == "" {
		return apikit.GrantIntentFenceResponse{}, fmt.Errorf("%w: grant_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.GrantIntentFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT grant_id FROM access_intent.grant_intent WHERE grant_id = $1 FOR SHARE`, grantID).Scan(&exists); err != nil {
		return apikit.GrantIntentFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO access_intent.grant_receipt (grant_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (grant_id) DO UPDATE SET
  fence_epoch = access_intent.grant_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, grantID, workerID).Scan(&epoch); err != nil {
		return apikit.GrantIntentFenceResponse{}, err
	}
	item, found, getErr := getGrantIntentFrom(ctx, tx, grantID)
	if getErr != nil {
		err = getErr
		return apikit.GrantIntentFenceResponse{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.GrantIntentFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.GrantIntentFenceResponse{}, err
	}
	return apikit.GrantIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordGrantReceipt persists a worker execution receipt conditional on the
// grant incarnation, current desired revision, state, and fence epoch.
func (w *Writer) RecordGrantReceipt(ctx context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.GrantReceipt{}, ErrDisabled
	}
	grantID = strings.TrimSpace(grantID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.TargetUID = strings.TrimSpace(req.TargetUID)
	req.SpecDigest = strings.TrimSpace(req.SpecDigest)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if grantID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validGrantOutcome(req.Outcome) {
		return apikit.GrantReceipt{}, fmt.Errorf("%w: grant_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.GrantReceipt{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		currentRev    string
		state         string
		currentEpoch  int64
		appliedRev    string
		currentUID    string
		currentGen    int64
		currentDigest string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  i.state,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.target_uid, ''),
  COALESCE(r.target_generation, 0),
  COALESCE(r.spec_digest, '')
FROM access_intent.grant_intent i
LEFT JOIN access_intent.grant_receipt r ON r.grant_id = i.grant_id
WHERE i.grant_id = $1
FOR UPDATE OF i`, grantID).Scan(&currentRev, &state, &currentEpoch, &appliedRev, &currentUID, &currentGen, &currentDigest)
	if err != nil {
		return apikit.GrantReceipt{}, err
	}
	if currentEpoch != req.FenceEpoch || currentRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.GrantReceipt{}, err
	}

	nextAppliedRev := appliedRev
	nextUID := currentUID
	nextGen := currentGen
	nextDigest := currentDigest
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false

	switch req.Outcome {
	case apikit.AccessIntentOutcomeApplied:
		if state != apikit.GrantIntentStateActive {
			err = ErrStaleReceipt
			return apikit.GrantReceipt{}, err
		}
		nextAppliedRev = req.DesiredRevisionID
		nextUID = req.TargetUID
		nextGen = req.TargetGeneration
		nextDigest = req.SpecDigest
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.AccessIntentOutcomeCleaned:
		if state != apikit.GrantIntentStateRevoked && state != apikit.GrantIntentStateDeleted {
			err = ErrStaleReceipt
			return apikit.GrantReceipt{}, err
		}
		nextAppliedRev = req.DesiredRevisionID
		if req.TargetUID != "" {
			nextUID = req.TargetUID
		}
		if req.SpecDigest != "" {
			nextDigest = req.SpecDigest
		}
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.AccessIntentOutcomeSuperseded:
		nextErrClass = ""
		nextErr = ""
	case apikit.AccessIntentOutcomeFailed, apikit.AccessIntentOutcomeTerminal:
		// Preserve prior applied_revision_id so failed retries never fabricate applied state.
	}

	var (
		out       apikit.GrantReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE access_intent.grant_receipt
SET
  applied_revision_id = $2,
  target_uid = $3,
  target_generation = $4,
  spec_digest = $5,
  outcome = $6,
  attempt = $7,
  worker_id = $8,
  last_error_class = $9,
  last_error = $10,
  applied_at = CASE WHEN $11 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE grant_id = $1 AND fence_epoch = $12
RETURNING fence_epoch, fence_holder, applied_revision_id, target_uid, target_generation, spec_digest, outcome, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		grantID,
		nextAppliedRev,
		nextUID,
		nextGen,
		nextDigest,
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
		&out.TargetUID,
		&out.TargetGeneration,
		&out.SpecDigest,
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
		return apikit.GrantReceipt{}, err
	}
	if err != nil {
		return apikit.GrantReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.GrantReceipt{}, err
	}
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		out.AppliedAt = &t
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

// ClaimObserverLease atomically increments the monotonic observer epoch for
// clusterKey so older observers cannot overwrite fresher observations.
func (w *Writer) ClaimObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error) {
	if w == nil || w.db == nil {
		return apikit.AccessIntentObserverLeaseResponse{}, ErrDisabled
	}
	clusterKey = strings.TrimSpace(clusterKey)
	if clusterKey == "" {
		clusterKey = defaultClusterKey
	}
	observerID = strings.TrimSpace(observerID)
	if observerID == "" {
		return apikit.AccessIntentObserverLeaseResponse{}, fmt.Errorf("%w: observer_id is required", ErrInvalidInput)
	}
	var epoch int64
	if err := w.db.QueryRowContext(ctx, `
INSERT INTO access_intent.observer_lease (cluster_key, observer_id, observer_epoch, updated_at)
VALUES ($1, $2, 1, clock_timestamp())
ON CONFLICT (cluster_key) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = access_intent.observer_lease.observer_epoch + 1,
  updated_at = EXCLUDED.updated_at
RETURNING observer_epoch`, clusterKey, observerID).Scan(&epoch); err != nil {
		return apikit.AccessIntentObserverLeaseResponse{}, err
	}
	return apikit.AccessIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: epoch,
	}, nil
}

// RecordGrantObservation persists independently observed Kubernetes CR and
// gateway replica propagation state without modifying desired intent or River jobs.
func (w *Writer) RecordGrantObservation(ctx context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error) {
	if w == nil || w.db == nil {
		return apikit.GrantObservation{}, ErrDisabled
	}
	grantID = strings.TrimSpace(grantID)
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
	req.PropagationStatus = strings.TrimSpace(req.PropagationStatus)
	if req.PropagationStatus == "" {
		req.PropagationStatus = apikit.GrantPropagationUnknown
	}
	if grantID == "" || req.ObserverID == "" || req.ObserverEpoch <= 0 || !validAccessHealth(req.Health) || !validPropagationStatus(req.PropagationStatus) {
		return apikit.GrantObservation{}, fmt.Errorf("%w: grant_id, observer_id, positive observer_epoch, valid health, and valid propagation_status are required", ErrInvalidInput)
	}
	if req.ServingReplicas < 0 || req.AcknowledgedReplicas < 0 || (req.ServingReplicas > 0 && req.AcknowledgedReplicas > req.ServingReplicas) {
		return apikit.GrantObservation{}, fmt.Errorf("%w: invalid replica acknowledgement counts", ErrInvalidInput)
	}
	verifiedAt := req.VerifiedAt.UTC()
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	evidence := req.GatewayEvidence
	if evidence == nil {
		evidence = []apikit.GrantGatewayReplicaEvidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return apikit.GrantObservation{}, err
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.GrantObservation{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev string
		state      string
		appliedRev string
		receiptUID string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  i.state,
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.target_uid, '')
FROM access_intent.grant_intent i
LEFT JOIN access_intent.grant_receipt r ON r.grant_id = i.grant_id
WHERE i.grant_id = $1
FOR SHARE OF i`, grantID).Scan(&desiredRev, &state, &appliedRev, &receiptUID)
	if err != nil {
		return apikit.GrantObservation{}, err
	}

	var leaseEpoch int64
	if scanErr := tx.QueryRowContext(ctx, `SELECT observer_epoch FROM access_intent.observer_lease WHERE cluster_key = $1`, req.ClusterKey).Scan(&leaseEpoch); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.GrantObservation{}, err
	}
	if leaseEpoch > 0 && req.ObserverEpoch < leaseEpoch {
		err = ErrStaleObservation
		return apikit.GrantObservation{}, err
	}

	var (
		existingEpoch      int64
		existingHealth     string
		existingProp       string
		existingTransition sql.NullTime
	)
	scanErr := tx.QueryRowContext(ctx, `
SELECT observer_epoch, health, propagation_status, last_transition_at
FROM access_intent.grant_observation
WHERE grant_id = $1
FOR UPDATE`, grantID).Scan(&existingEpoch, &existingHealth, &existingProp, &existingTransition)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.GrantObservation{}, err
	}
	if existingEpoch > 0 && req.ObserverEpoch < existingEpoch {
		err = ErrStaleObservation
		return apikit.GrantObservation{}, err
	}

	if req.ObservedRevisionID != "" && req.ObservedRevisionID != desiredRev && req.ObservedRevisionID != appliedRev {
		err = ErrStaleObservation
		return apikit.GrantObservation{}, err
	}
	// Never allow Verified propagation unless every serving replica acknowledged
	// the current desired revision and projection was applied.
	if req.PropagationStatus == apikit.GrantPropagationVerified {
		if req.ServingReplicas <= 0 || req.AcknowledgedReplicas != req.ServingReplicas {
			err = ErrStaleObservation
			return apikit.GrantObservation{}, err
		}
		if appliedRev == "" || appliedRev != desiredRev || req.ObservedRevisionID != desiredRev {
			err = ErrStaleObservation
			return apikit.GrantObservation{}, err
		}
		if state == apikit.GrantIntentStateActive && receiptUID != "" && req.TargetUID != receiptUID {
			err = ErrStaleObservation
			return apikit.GrantObservation{}, err
		}
	}

	transitionAt := verifiedAt
	if existingTransition.Valid && existingHealth == req.Health && existingProp == req.PropagationStatus {
		transitionAt = existingTransition.Time.UTC()
	}

	var (
		out         apikit.GrantObservation
		outEvidence []byte
	)
	err = tx.QueryRowContext(ctx, `
INSERT INTO access_intent.grant_observation (
  grant_id, observer_id, observer_epoch, observed_revision_id, target_uid,
  health, reason, message, propagation_status, serving_replicas, acknowledged_replicas,
  gateway_evidence, last_transition_at, last_verified_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, clock_timestamp())
ON CONFLICT (grant_id) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = EXCLUDED.observer_epoch,
  observed_revision_id = EXCLUDED.observed_revision_id,
  target_uid = EXCLUDED.target_uid,
  health = EXCLUDED.health,
  reason = EXCLUDED.reason,
  message = EXCLUDED.message,
  propagation_status = EXCLUDED.propagation_status,
  serving_replicas = EXCLUDED.serving_replicas,
  acknowledged_replicas = EXCLUDED.acknowledged_replicas,
  gateway_evidence = EXCLUDED.gateway_evidence,
  last_transition_at = EXCLUDED.last_transition_at,
  last_verified_at = EXCLUDED.last_verified_at,
  updated_at = EXCLUDED.updated_at
RETURNING observer_id, observer_epoch, observed_revision_id, target_uid, health, reason, message, propagation_status, serving_replicas, acknowledged_replicas, gateway_evidence, last_transition_at, last_verified_at, updated_at`,
		grantID,
		req.ObserverID,
		req.ObserverEpoch,
		req.ObservedRevisionID,
		req.TargetUID,
		req.Health,
		req.Reason,
		req.Message,
		req.PropagationStatus,
		req.ServingReplicas,
		req.AcknowledgedReplicas,
		string(evidenceJSON),
		transitionAt,
		verifiedAt,
	).Scan(
		&out.ObserverID,
		&out.ObserverEpoch,
		&out.ObservedRevisionID,
		&out.TargetUID,
		&out.Health,
		&out.Reason,
		&out.Message,
		&out.PropagationStatus,
		&out.ServingReplicas,
		&out.AcknowledgedReplicas,
		&outEvidence,
		&out.LastTransitionAt,
		&out.LastVerifiedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		return apikit.GrantObservation{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.GrantObservation{}, err
	}
	if len(outEvidence) > 0 {
		_ = json.Unmarshal(outEvidence, &out.GatewayEvidence)
	}
	out.LastTransitionAt = out.LastTransitionAt.UTC()
	out.LastVerifiedAt = out.LastVerifiedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func validGrantOutcome(v string) bool {
	switch v {
	case apikit.AccessIntentOutcomeApplied,
		apikit.AccessIntentOutcomeCleaned,
		apikit.AccessIntentOutcomeSuperseded,
		apikit.AccessIntentOutcomeFailed,
		apikit.AccessIntentOutcomeTerminal:
		return true
	default:
		return false
	}
}

func validAccessHealth(v string) bool {
	switch v {
	case apikit.AccessIntentHealthPending,
		apikit.AccessIntentHealthProgressing,
		apikit.AccessIntentHealthReady,
		apikit.AccessIntentHealthDegraded,
		apikit.AccessIntentHealthRevoked,
		apikit.AccessIntentHealthDeleted,
		apikit.AccessIntentHealthUnknown:
		return true
	default:
		return false
	}
}

func validPropagationStatus(v string) bool {
	switch v {
	case apikit.GrantPropagationVerified,
		apikit.GrantPropagationPending,
		apikit.GrantPropagationUnverified,
		apikit.GrantPropagationUnknown:
		return true
	default:
		return false
	}
}
