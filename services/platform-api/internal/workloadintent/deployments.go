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

const deploymentSelectSQL = `
SELECT
  i.deployment_id,
  i.namespace,
  i.name,
  i.team_id,
  i.desired_revision_id,
  i.deleted,
  i.image,
  i.version,
  i.port,
  i.replicas,
  i.spec_digest,
  i.target_deployment_uid,
  i.updated_at,
  r.deployment_id IS NOT NULL,
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
  o.deployment_id IS NOT NULL,
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
FROM workload_intent.deployment_intent i
LEFT JOIN workload_intent.deployment_receipt r ON r.deployment_id = i.deployment_id
LEFT JOIN workload_intent.deployment_observation o ON o.deployment_id = i.deployment_id`

func scanDeploymentIntent(scanner rowScanner) (apikit.DeploymentIntent, error) {
	var (
		item            apikit.DeploymentIntent
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
		&item.DeploymentID,
		&item.Namespace,
		&item.Name,
		&item.TeamID,
		&item.DesiredRevisionID,
		&item.Deleted,
		&item.Image,
		&item.Version,
		&item.Port,
		&item.Replicas,
		&item.SpecDigest,
		&item.TargetDeploymentUID,
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
		return apikit.DeploymentIntent{}, err
	}
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.Receipt = buildReceipt(hasReceipt, receipt, receiptApplied, receiptUpdated)
	item.Observation = buildObservation(hasObs, obs, obsCondsJSON, obsTransitionAt, obsVerifiedAt, obsUpdatedAt)
	return item, nil
}

// ListDeploymentIntents returns all recorded raw Deployment intents.
func (w *Writer) ListDeploymentIntents(ctx context.Context) ([]apikit.DeploymentIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, deploymentSelectSQL+` ORDER BY i.updated_at DESC, i.deployment_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.DeploymentIntent, 0)
	for rows.Next() {
		item, err := scanDeploymentIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetDeploymentIntent returns one raw Deployment intent by deployment_id.
func (w *Writer) GetDeploymentIntent(ctx context.Context, deploymentID string) (apikit.DeploymentIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.DeploymentIntent{}, false, ErrDisabled
	}
	return getDeploymentIntentFrom(ctx, w.db, deploymentID)
}

func getDeploymentIntentFrom(ctx context.Context, q queryRower, deploymentID string) (apikit.DeploymentIntent, bool, error) {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return apikit.DeploymentIntent{}, false, nil
	}
	item, err := scanDeploymentIntent(q.QueryRowContext(ctx, deploymentSelectSQL+` WHERE i.deployment_id = $1`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.DeploymentIntent{}, false, nil
	}
	if err != nil {
		return apikit.DeploymentIntent{}, false, err
	}
	return item, true, nil
}

// UpsertDeploymentIntent atomically records desired raw Deployment/Service intent
// and enqueues reconcile_deployment on the workloads queue in one transaction.
func (w *Writer) UpsertDeploymentIntent(ctx context.Context, req apikit.DeploymentIntentUpsertRequest) (apikit.DeploymentIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.DeploymentIntent{}, ErrDisabled
	}
	req.DeploymentID = strings.TrimSpace(req.DeploymentID)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.Image = strings.TrimSpace(req.Image)
	req.Version = strings.TrimSpace(req.Version)
	if req.DeploymentID == "" {
		req.DeploymentID = uuid.NewString()
	}
	if req.Namespace == "" || req.Name == "" || req.Image == "" {
		return apikit.DeploymentIntent{}, fmt.Errorf("%w: namespace, name, and image are required", ErrInvalidInput)
	}
	if req.Port <= 0 {
		req.Port = 8088
	}
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	digest := strings.TrimSpace(req.SpecDigest)
	if digest == "" {
		specPayload, _ := json.Marshal(struct {
			Image    string `json:"image"`
			Version  string `json:"version"`
			Port     int32  `json:"port"`
			Replicas int32  `json:"replicas"`
		}{req.Image, req.Version, req.Port, req.Replicas})
		digest = digestBytes(specPayload)
	}
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.DeploymentIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.deployment_intent (
  deployment_id, namespace, name, team_id, desired_revision_id, deleted,
  image, version, port, replicas, spec_digest, target_deployment_uid, updated_at
) VALUES ($1, $2, $3, $4, $5, false, $6, $7, $8, $9, $10, '', clock_timestamp())
ON CONFLICT (deployment_id) DO UPDATE SET
  namespace = EXCLUDED.namespace,
  name = EXCLUDED.name,
  team_id = EXCLUDED.team_id,
  desired_revision_id = EXCLUDED.desired_revision_id,
  deleted = false,
  image = EXCLUDED.image,
  version = EXCLUDED.version,
  port = EXCLUDED.port,
  replicas = EXCLUDED.replicas,
  spec_digest = EXCLUDED.spec_digest,
  updated_at = EXCLUDED.updated_at
RETURNING updated_at`,
		req.DeploymentID, req.Namespace, req.Name, req.TeamID, revisionID,
		req.Image, req.Version, req.Port, req.Replicas, digest,
	).Scan(&updatedAt)
	if err != nil {
		return apikit.DeploymentIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileDeploymentJobArgs{
		DeploymentID:      req.DeploymentID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueWorkloads}); err != nil {
		return apikit.DeploymentIntent{}, fmt.Errorf("enqueue deployment reconciliation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return apikit.DeploymentIntent{}, err
	}
	return apikit.DeploymentIntent{
		DeploymentID:      req.DeploymentID,
		Namespace:         req.Namespace,
		Name:              req.Name,
		TeamID:            req.TeamID,
		DesiredRevisionID: revisionID,
		Deleted:           false,
		Image:             req.Image,
		Version:           req.Version,
		Port:              req.Port,
		Replicas:          req.Replicas,
		SpecDigest:        digest,
		UpdatedAt:         updatedAt.UTC(),
	}, nil
}

// DeleteDeploymentIntent marks a raw Deployment intent as deleted and enqueues
// reconcile_deployment on workloads in one transaction.
func (w *Writer) DeleteDeploymentIntent(ctx context.Context, deploymentID string, req apikit.DeploymentIntentDeleteRequest) (apikit.DeploymentIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.DeploymentIntent{}, ErrDisabled
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return apikit.DeploymentIntent{}, fmt.Errorf("%w: deployment_id is required", ErrInvalidInput)
	}
	targetUID := strings.TrimSpace(req.TargetDeploymentUID)
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.DeploymentIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existingUID, receiptUID string
	err = tx.QueryRowContext(ctx, `
SELECT i.target_deployment_uid, COALESCE(r.target_uid, '')
FROM workload_intent.deployment_intent i
LEFT JOIN workload_intent.deployment_receipt r ON r.deployment_id = i.deployment_id
WHERE i.deployment_id = $1
FOR UPDATE OF i`, deploymentID).Scan(&existingUID, &receiptUID)
	if err != nil {
		return apikit.DeploymentIntent{}, err
	}
	if targetUID == "" {
		if existingUID != "" {
			targetUID = existingUID
		} else {
			targetUID = receiptUID
		}
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.deployment_intent
SET
  desired_revision_id = $2,
  deleted = true,
  target_deployment_uid = $3,
  updated_at = clock_timestamp()
WHERE deployment_id = $1`, deploymentID, revisionID, targetUID); err != nil {
		return apikit.DeploymentIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileDeploymentJobArgs{
		DeploymentID:      deploymentID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueWorkloads}); err != nil {
		return apikit.DeploymentIntent{}, fmt.Errorf("enqueue deployment deletion: %w", err)
	}

	item, ok, err := getDeploymentIntentFrom(ctx, tx, deploymentID)
	if err != nil {
		return apikit.DeploymentIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.DeploymentIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.DeploymentIntent{}, err
	}
	return item, nil
}

// ClaimDeploymentFence bumps the per-deployment execution fence epoch and
// returns the authoritative desired intent.
func (w *Writer) ClaimDeploymentFence(ctx context.Context, deploymentID, workerID string) (apikit.DeploymentIntentFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.DeploymentIntentFenceResponse{}, ErrDisabled
	}
	deploymentID = strings.TrimSpace(deploymentID)
	workerID = strings.TrimSpace(workerID)
	if deploymentID == "" || workerID == "" {
		return apikit.DeploymentIntentFenceResponse{}, fmt.Errorf("%w: deployment_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT deployment_id FROM workload_intent.deployment_intent WHERE deployment_id = $1 FOR SHARE`, deploymentID).Scan(&exists); err != nil {
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.deployment_receipt (deployment_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (deployment_id) DO UPDATE SET
  fence_epoch = workload_intent.deployment_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, deploymentID, workerID).Scan(&epoch); err != nil {
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	item, ok, err := getDeploymentIntentFrom(ctx, tx, deploymentID)
	if err != nil {
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.DeploymentIntentFenceResponse{}, err
	}
	return apikit.DeploymentIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordDeploymentReceipt records a fenced worker execution receipt for one raw deployment.
func (w *Writer) RecordDeploymentReceipt(ctx context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadExecutionReceipt{}, ErrDisabled
	}
	deploymentID = strings.TrimSpace(deploymentID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.TargetUID = strings.TrimSpace(req.TargetUID)
	req.SpecDigest = strings.TrimSpace(req.SpecDigest)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if deploymentID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validOutcome(req.Outcome) {
		return apikit.WorkloadExecutionReceipt{}, fmt.Errorf("%w: deployment_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
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
FROM workload_intent.deployment_intent i
JOIN workload_intent.deployment_receipt r ON r.deployment_id = i.deployment_id
WHERE i.deployment_id = $1
FOR UPDATE OF r`, deploymentID).Scan(&desiredRev, &currentFence, &appliedRev, &existingTargetUID, &existingGen, &existingDigest)
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
UPDATE workload_intent.deployment_receipt
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
WHERE deployment_id = $1 AND fence_epoch = $12
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, target_uid, target_generation, spec_digest, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		deploymentID, nextApplied, req.Outcome, nextUID, nextGen, nextDigest, req.Attempt, req.WorkerID, nextErrClass, nextErr, markAppliedAt, req.FenceEpoch,
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
UPDATE workload_intent.deployment_intent SET target_deployment_uid = $2 WHERE deployment_id = $1 AND target_deployment_uid = ''`,
			deploymentID, nextUID); err != nil {
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

// RecordDeploymentObservation persists independent health observation for a
// raw Deployment without mutating desired intent, receipts, or River jobs.
func (w *Writer) RecordDeploymentObservation(ctx context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadStatusObservation{}, ErrDisabled
	}
	deploymentID = strings.TrimSpace(deploymentID)
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
	if deploymentID == "" || req.ObserverID == "" || req.ObserverEpoch <= 0 || !validHealth(req.Health) {
		return apikit.WorkloadStatusObservation{}, fmt.Errorf("%w: deployment_id, observer_id, positive observer_epoch, and valid health are required", ErrInvalidInput)
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
		deleted    bool
		appliedRev string
		receiptUID string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  i.deleted,
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.target_uid, '')
FROM workload_intent.deployment_intent i
LEFT JOIN workload_intent.deployment_receipt r ON r.deployment_id = i.deployment_id
WHERE i.deployment_id = $1
FOR SHARE OF i`, deploymentID).Scan(&desiredRev, &deleted, &appliedRev, &receiptUID)
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
FROM workload_intent.deployment_observation
WHERE deployment_id = $1
FOR UPDATE`, deploymentID).Scan(&existingEpoch, &existingHealth, &existingReason, &existingTransition)
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
		if deleted || appliedRev == "" || appliedRev != desiredRev || req.ObservedRevisionID != appliedRev {
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
INSERT INTO workload_intent.deployment_observation (
  deployment_id, observer_id, observer_epoch, observed_revision_id, target_uid, observed_generation,
  health, reason, message, conditions, last_transition_at, last_verified_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, clock_timestamp())
ON CONFLICT (deployment_id) DO UPDATE SET
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
		deploymentID, req.ObserverID, req.ObserverEpoch, req.ObservedRevisionID, req.TargetUID, req.ObservedGeneration,
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
