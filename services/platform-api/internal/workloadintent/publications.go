package workloadintent

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

const publicationSelectSQL = `
SELECT
  i.publication_id,
  i.team_id,
  i.namespace,
  i.registry,
  i.repository,
  i.tag,
  i.mutable_tag_policy,
  i.upload_state,
  i.artifact_ref,
  i.artifact_sha256,
  i.artifact_size_bytes,
  i.helper_name,
  i.verified_digest,
  i.desired_revision_id,
  i.status,
  i.updated_at,
  r.publication_id IS NOT NULL,
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
FROM workload_intent.image_publication_intent i
LEFT JOIN workload_intent.image_publication_receipt r ON r.publication_id = i.publication_id`

func scanPublicationIntent(scanner rowScanner) (apikit.ImagePublicationIntent, error) {
	var (
		item           apikit.ImagePublicationIntent
		hasReceipt     bool
		receipt        apikit.WorkloadExecutionReceipt
		receiptApplied sql.NullTime
		receiptUpdated sql.NullTime
	)
	if err := scanner.Scan(
		&item.PublicationID,
		&item.TeamID,
		&item.Namespace,
		&item.Registry,
		&item.Repository,
		&item.Tag,
		&item.MutableTagPolicy,
		&item.UploadState,
		&item.ArtifactRef,
		&item.ArtifactSHA256,
		&item.ArtifactSizeBytes,
		&item.HelperName,
		&item.VerifiedDigest,
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
		return apikit.ImagePublicationIntent{}, err
	}
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.Receipt = buildReceipt(hasReceipt, receipt, receiptApplied, receiptUpdated)
	return item, nil
}

// ListImagePublicationIntents returns all recorded image publication intents.
func (w *Writer) ListImagePublicationIntents(ctx context.Context) ([]apikit.ImagePublicationIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, publicationSelectSQL+` ORDER BY i.updated_at DESC, i.publication_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.ImagePublicationIntent, 0)
	for rows.Next() {
		item, err := scanPublicationIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetImagePublicationIntent returns one image publication intent by publication_id.
func (w *Writer) GetImagePublicationIntent(ctx context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.ImagePublicationIntent{}, false, ErrDisabled
	}
	return getPublicationIntentFrom(ctx, w.db, publicationID)
}

func getPublicationIntentFrom(ctx context.Context, q queryRower, publicationID string) (apikit.ImagePublicationIntent, bool, error) {
	publicationID = strings.TrimSpace(publicationID)
	if publicationID == "" {
		return apikit.ImagePublicationIntent{}, false, nil
	}
	item, err := scanPublicationIntent(q.QueryRowContext(ctx, publicationSelectSQL+` WHERE i.publication_id = $1`, publicationID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.ImagePublicationIntent{}, false, nil
	}
	if err != nil {
		return apikit.ImagePublicationIntent{}, false, err
	}
	return item, true, nil
}

// BeginImagePublication registers a staged upload in Uploading state. It NEVER
// enqueues a River job until FinalizeImagePublication commits immutable staging
// metadata, and enforces mutable-tag overlap policy.
func (w *Writer) BeginImagePublication(ctx context.Context, req apikit.ImagePublicationBeginRequest) (apikit.ImagePublicationIntent, error) {
	if w == nil || w.db == nil {
		return apikit.ImagePublicationIntent{}, ErrDisabled
	}
	req.PublicationID = strings.TrimSpace(req.PublicationID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Registry = strings.TrimSpace(req.Registry)
	req.Repository = strings.TrimSpace(req.Repository)
	req.Tag = strings.TrimSpace(req.Tag)
	req.MutableTagPolicy = strings.TrimSpace(req.MutableTagPolicy)
	if req.PublicationID == "" {
		req.PublicationID = uuid.NewString()
	}
	if req.MutableTagPolicy == "" {
		req.MutableTagPolicy = apikit.MutableTagPolicyRejectOverlap
	}
	if req.MutableTagPolicy != apikit.MutableTagPolicyRejectOverlap && req.MutableTagPolicy != apikit.MutableTagPolicyAllowOverlap {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("%w: unsupported mutable_tag_policy", ErrInvalidInput)
	}
	if req.Namespace == "" || req.Registry == "" || req.Repository == "" || req.Tag == "" {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("%w: namespace, registry, repository, and tag are required", ErrInvalidInput)
	}
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if req.MutableTagPolicy == apikit.MutableTagPolicyRejectOverlap {
		var existingID string
		scanErr := tx.QueryRowContext(ctx, `
SELECT publication_id FROM workload_intent.image_publication_intent
WHERE registry = $1 AND repository = $2 AND tag = $3
  AND status IN ('Uploading', 'Queued', 'Publishing')
LIMIT 1`, req.Registry, req.Repository, req.Tag).Scan(&existingID)
		if scanErr == nil && existingID != "" {
			err = ErrTagOverlapConflict
			return apikit.ImagePublicationIntent{}, err
		}
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			err = scanErr
			return apikit.ImagePublicationIntent{}, err
		}
	}

	var updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.image_publication_intent (
  publication_id, team_id, namespace, registry, repository, tag,
  mutable_tag_policy, upload_state, desired_revision_id, status, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, clock_timestamp())
RETURNING updated_at`,
		req.PublicationID, req.TeamID, req.Namespace, req.Registry, req.Repository, req.Tag,
		req.MutableTagPolicy, apikit.ImageUploadStateUploading, revisionID, apikit.ImagePublicationStatusUploading,
	).Scan(&updatedAt)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	return apikit.ImagePublicationIntent{
		PublicationID:     req.PublicationID,
		TeamID:            req.TeamID,
		Namespace:         req.Namespace,
		Registry:          req.Registry,
		Repository:        req.Repository,
		Tag:               req.Tag,
		MutableTagPolicy:  req.MutableTagPolicy,
		UploadState:       apikit.ImageUploadStateUploading,
		DesiredRevisionID: revisionID,
		Status:            apikit.ImagePublicationStatusUploading,
		UpdatedAt:         updatedAt.UTC(),
	}, nil
}

// FinalizeImagePublication commits immutable staged artifact coordinates and
// enqueues reconcile_image_publication on the dedicated registry_publication
// queue in ONE SQL transaction. Unfinalized uploads are never queued.
func (w *Writer) FinalizeImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationFinalizeRequest) (apikit.ImagePublicationIntent, error) {
	if w == nil || w.db == nil || w.enqueuer == nil {
		return apikit.ImagePublicationIntent{}, ErrDisabled
	}
	publicationID = strings.TrimSpace(publicationID)
	req.ArtifactRef = strings.TrimSpace(req.ArtifactRef)
	req.ArtifactSHA256 = strings.ToLower(strings.TrimSpace(req.ArtifactSHA256))
	if publicationID == "" || req.ArtifactRef == "" || req.ArtifactSHA256 == "" || req.ArtifactSizeBytes <= 0 {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("%w: publication_id, artifact_ref, artifact_sha256, and positive artifact_size_bytes are required", ErrInvalidInput)
	}
	revisionID := uuid.NewString()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var uploadState, status string
	err = tx.QueryRowContext(ctx, `SELECT upload_state, status FROM workload_intent.image_publication_intent WHERE publication_id = $1 FOR UPDATE`, publicationID).Scan(&uploadState, &status)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	if uploadState != apikit.ImageUploadStateUploading || status != apikit.ImagePublicationStatusUploading {
		err = ErrInvalidStateTransition
		return apikit.ImagePublicationIntent{}, err
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.image_publication_intent
SET
  upload_state = $2,
  artifact_ref = $3,
  artifact_sha256 = $4,
  artifact_size_bytes = $5,
  desired_revision_id = $6,
  status = $7,
  updated_at = clock_timestamp()
WHERE publication_id = $1`,
		publicationID, apikit.ImageUploadStateFinalized, req.ArtifactRef, req.ArtifactSHA256,
		req.ArtifactSizeBytes, revisionID, apikit.ImagePublicationStatusQueued,
	); err != nil {
		return apikit.ImagePublicationIntent{}, err
	}

	reqID, tp, ts := traceHeaders(ctx)
	if err = w.enqueuer.InsertTx(ctx, tx, ReconcileImagePublicationJobArgs{
		PublicationID:     publicationID,
		DesiredRevisionID: revisionID,
		RequestID:         reqID,
		Traceparent:       tp,
		Tracestate:        ts,
	}, &river.InsertOpts{Queue: QueueRegistryPublication}); err != nil {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("enqueue image publication: %w", err)
	}

	item, ok, err := getPublicationIntentFrom(ctx, tx, publicationID)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ImagePublicationIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	return item, nil
}

// AbortImagePublication marks an unfinalized publication Aborted or Expired without queuing work.
func (w *Writer) AbortImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationAbortRequest) (apikit.ImagePublicationIntent, error) {
	if w == nil || w.db == nil {
		return apikit.ImagePublicationIntent{}, ErrDisabled
	}
	publicationID = strings.TrimSpace(publicationID)
	if publicationID == "" {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("%w: publication_id is required", ErrInvalidInput)
	}
	nextUploadState := strings.TrimSpace(req.UploadState)
	if nextUploadState == "" {
		nextUploadState = apikit.ImageUploadStateAborted
	}
	if nextUploadState != apikit.ImageUploadStateAborted && nextUploadState != apikit.ImageUploadStateExpired {
		return apikit.ImagePublicationIntent{}, fmt.Errorf("%w: upload_state must be Aborted or Expired", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var currentStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM workload_intent.image_publication_intent WHERE publication_id = $1 FOR UPDATE`, publicationID).Scan(&currentStatus)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	if currentStatus == apikit.ImagePublicationStatusPublished {
		err = ErrInvalidStateTransition
		return apikit.ImagePublicationIntent{}, err
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.image_publication_intent
SET upload_state = $2, status = $3, updated_at = clock_timestamp()
WHERE publication_id = $1`, publicationID, nextUploadState, apikit.ImagePublicationStatusAborted); err != nil {
		return apikit.ImagePublicationIntent{}, err
	}

	item, ok, err := getPublicationIntentFrom(ctx, tx, publicationID)
	if err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ImagePublicationIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ImagePublicationIntent{}, err
	}
	return item, nil
}

// ClaimImagePublicationFence bumps the execution fence for a publication and
// transitions Queued -> Publishing when claimed.
func (w *Writer) ClaimImagePublicationFence(ctx context.Context, publicationID, workerID string) (apikit.ImagePublicationFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.ImagePublicationFenceResponse{}, ErrDisabled
	}
	publicationID = strings.TrimSpace(publicationID)
	workerID = strings.TrimSpace(workerID)
	if publicationID == "" || workerID == "" {
		return apikit.ImagePublicationFenceResponse{}, fmt.Errorf("%w: publication_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.ImagePublicationFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM workload_intent.image_publication_intent WHERE publication_id = $1 FOR UPDATE`, publicationID).Scan(&status); err != nil {
		return apikit.ImagePublicationFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO workload_intent.image_publication_receipt (publication_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (publication_id) DO UPDATE SET
  fence_epoch = workload_intent.image_publication_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, publicationID, workerID).Scan(&epoch); err != nil {
		return apikit.ImagePublicationFenceResponse{}, err
	}
	if status == apikit.ImagePublicationStatusQueued {
		if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.image_publication_intent SET status = $2, updated_at = clock_timestamp() WHERE publication_id = $1`,
			publicationID, apikit.ImagePublicationStatusPublishing); err != nil {
			return apikit.ImagePublicationFenceResponse{}, err
		}
	}

	item, ok, err := getPublicationIntentFrom(ctx, tx, publicationID)
	if err != nil {
		return apikit.ImagePublicationFenceResponse{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.ImagePublicationFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.ImagePublicationFenceResponse{}, err
	}
	return apikit.ImagePublicationFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordImagePublicationReceipt records a fenced publication receipt, helper
// identity, verified manifest digest, and resulting publication status.
func (w *Writer) RecordImagePublicationReceipt(ctx context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadExecutionReceipt{}, ErrDisabled
	}
	publicationID = strings.TrimSpace(publicationID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.Status = strings.TrimSpace(req.Status)
	req.HelperName = strings.TrimSpace(req.HelperName)
	req.VerifiedDigest = strings.TrimSpace(req.VerifiedDigest)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if publicationID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validOutcome(req.Outcome) {
		return apikit.WorkloadExecutionReceipt{}, fmt.Errorf("%w: publication_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}
	// Published status ALWAYS requires a verified manifest digest from the registry.
	if (req.Outcome == apikit.WorkloadIntentOutcomeApplied || req.Status == apikit.ImagePublicationStatusPublished) && req.VerifiedDigest == "" {
		return apikit.WorkloadExecutionReceipt{}, fmt.Errorf("%w: verified_digest is required to mark publication Published", ErrInvalidInput)
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
		curHelper    string
		curDigest    string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  r.fence_epoch,
  r.applied_revision_id,
  i.status,
  i.helper_name,
  i.verified_digest
FROM workload_intent.image_publication_intent i
JOIN workload_intent.image_publication_receipt r ON r.publication_id = i.publication_id
WHERE i.publication_id = $1
FOR UPDATE OF i, r`, publicationID).Scan(&desiredRev, &currentFence, &appliedRev, &curStatus, &curHelper, &curDigest)
	if err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}
	if currentFence != req.FenceEpoch || desiredRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.WorkloadExecutionReceipt{}, err
	}

	nextApplied := appliedRev
	nextStatus := curStatus
	nextHelper := curHelper
	if req.HelperName != "" {
		nextHelper = req.HelperName
	}
	nextDigest := curDigest
	if req.VerifiedDigest != "" {
		nextDigest = req.VerifiedDigest
	}
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false

	switch req.Outcome {
	case apikit.WorkloadIntentOutcomeApplied:
		nextApplied = req.DesiredRevisionID
		nextStatus = apikit.ImagePublicationStatusPublished
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.WorkloadIntentOutcomeFailed, apikit.WorkloadIntentOutcomeTerminal:
		if req.Status != "" {
			nextStatus = req.Status
		} else {
			nextStatus = apikit.ImagePublicationStatusFailed
		}
	default:
		if req.Status != "" {
			nextStatus = req.Status
		}
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE workload_intent.image_publication_intent
SET
  helper_name = $2,
  verified_digest = $3,
  status = $4,
  updated_at = clock_timestamp()
WHERE publication_id = $1`, publicationID, nextHelper, nextDigest, nextStatus); err != nil {
		return apikit.WorkloadExecutionReceipt{}, err
	}

	var (
		out       apikit.WorkloadExecutionReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE workload_intent.image_publication_receipt
SET
  applied_revision_id = $2,
  outcome = $3,
  spec_digest = $4,
  attempt = $5,
  worker_id = $6,
  last_error_class = $7,
  last_error = $8,
  applied_at = CASE WHEN $9 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE publication_id = $1 AND fence_epoch = $10
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, target_uid, target_generation, spec_digest, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		publicationID, nextApplied, req.Outcome, nextDigest, req.Attempt, req.WorkerID, nextErrClass, nextErr, markAppliedAt, req.FenceEpoch,
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
