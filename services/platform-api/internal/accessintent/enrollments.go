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
	"mcp-runtime/pkg/sessionstate"
)

const enrollmentSelectColumns = `
SELECT
  i.operation_id,
  i.session_uid,
  i.server_uid,
  i.agent_id,
  i.namespace,
  i.session_name,
  i.csr_sha256,
  i.desired_revision_id,
  i.state,
  i.certificate_request_name,
  i.created_at,
  i.updated_at,
  i.activated_at,
  r.operation_id IS NOT NULL,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.fence_holder, ''),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.certificate_request_name, ''),
  COALESCE(r.certificate_request_uid, ''),
  COALESCE(r.outcome, ''),
  COALESCE(r.attempt, 0),
  COALESCE(r.worker_id, ''),
  COALESCE(r.last_error_class, ''),
  COALESCE(r.last_error, ''),
  r.applied_at,
  r.updated_at
FROM access_intent.enrollment_intent i
LEFT JOIN access_intent.enrollment_receipt r ON r.operation_id = i.operation_id`

func scanEnrollmentIntent(scanner rowScanner) (apikit.EnrollmentIntent, error) {
	var (
		item           apikit.EnrollmentIntent
		activatedAt    sql.NullTime
		hasReceipt     bool
		receipt        apikit.EnrollmentReceipt
		receiptApplied sql.NullTime
		receiptUpdated sql.NullTime
	)
	if err := scanner.Scan(
		&item.OperationID,
		&item.SessionUID,
		&item.ServerUID,
		&item.AgentID,
		&item.Namespace,
		&item.SessionName,
		&item.CSRSHA256,
		&item.DesiredRevisionID,
		&item.State,
		&item.CertificateRequestName,
		&item.CreatedAt,
		&item.UpdatedAt,
		&activatedAt,
		&hasReceipt,
		&receipt.FenceEpoch,
		&receipt.FenceHolder,
		&receipt.AppliedRevisionID,
		&receipt.CertificateRequestName,
		&receipt.CertificateRequestUID,
		&receipt.Outcome,
		&receipt.Attempt,
		&receipt.WorkerID,
		&receipt.LastErrorClass,
		&receipt.LastError,
		&receiptApplied,
		&receiptUpdated,
	); err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if activatedAt.Valid {
		t := activatedAt.Time.UTC()
		item.ActivatedAt = &t
	}
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

// CreateEnrollmentIntent idempotently creates a durable adapter enrollment
// operation keyed to (session_uid, server_uid, csr_sha256) with a deterministic
// CertificateRequest name and schedules reconcile_enrollment in ONE transaction.
// Private keys and raw certificate secrets are never accepted or persisted.
func (w *Writer) CreateEnrollmentIntent(ctx context.Context, req apikit.EnrollmentIntentCreateRequest) (apikit.EnrollmentIntent, bool, error) {
	if w == nil || w.db == nil || w.queue == nil {
		return apikit.EnrollmentIntent{}, false, ErrDisabled
	}
	req.SessionUID = strings.TrimSpace(req.SessionUID)
	req.ServerUID = strings.TrimSpace(req.ServerUID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.SessionName = strings.TrimSpace(req.SessionName)
	req.CSRSHA256 = strings.ToLower(strings.TrimSpace(req.CSRSHA256))
	if req.SessionUID == "" || req.ServerUID == "" || req.Namespace == "" || req.SessionName == "" {
		return apikit.EnrollmentIntent{}, false, fmt.Errorf("%w: session_uid, server_uid, namespace, and session_name are required", ErrInvalidInput)
	}
	if err := validateCSRSHA256(req.CSRSHA256); err != nil {
		return apikit.EnrollmentIntent{}, false, err
	}

	opID := uuid.NewString()
	revisionID := uuid.NewString()
	crName := DeterministicCertificateRequestName(req.SessionUID, req.ServerUID, req.CSRSHA256)

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.EnrollmentIntent{}, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var insertedID string
	scanErr := tx.QueryRowContext(ctx, `
INSERT INTO access_intent.enrollment_intent (
  operation_id, session_uid, server_uid, agent_id, namespace, session_name,
  csr_sha256, desired_revision_id, state, certificate_request_name,
  created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, clock_timestamp(), clock_timestamp())
ON CONFLICT (session_uid, server_uid, csr_sha256) DO NOTHING
RETURNING operation_id`,
		opID, req.SessionUID, req.ServerUID, req.AgentID, req.Namespace, req.SessionName,
		req.CSRSHA256, revisionID, apikit.EnrollmentStatePending, crName,
	).Scan(&insertedID)

	created := false
	switch {
	case scanErr == nil:
		created = true
		if _, err = w.queue.InsertTx(ctx, tx, newReconcileEnrollmentJob(ctx, insertedID, revisionID), &river.InsertOpts{Queue: QueueAccessControl}); err != nil {
			return apikit.EnrollmentIntent{}, false, fmt.Errorf("enqueue enrollment reconciliation: %w", err)
		}
	case errors.Is(scanErr, sql.ErrNoRows):
		if err = tx.QueryRowContext(ctx, `
SELECT operation_id FROM access_intent.enrollment_intent
WHERE session_uid = $1 AND server_uid = $2 AND csr_sha256 = $3`,
			req.SessionUID, req.ServerUID, req.CSRSHA256,
		).Scan(&insertedID); err != nil {
			return apikit.EnrollmentIntent{}, false, err
		}
	default:
		err = scanErr
		return apikit.EnrollmentIntent{}, false, err
	}

	item, found, getErr := getEnrollmentIntentFrom(ctx, tx, insertedID)
	if getErr != nil {
		err = getErr
		return apikit.EnrollmentIntent{}, false, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.EnrollmentIntent{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.EnrollmentIntent{}, false, err
	}
	return item, created, nil
}

// ListEnrollmentIntents returns all durable adapter enrollment operations.
func (w *Writer) ListEnrollmentIntents(ctx context.Context) ([]apikit.EnrollmentIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, enrollmentSelectColumns+` ORDER BY i.updated_at DESC, i.operation_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.EnrollmentIntent, 0)
	for rows.Next() {
		item, err := scanEnrollmentIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetEnrollmentIntent returns one durable adapter enrollment operation.
func (w *Writer) GetEnrollmentIntent(ctx context.Context, opID string) (apikit.EnrollmentIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.EnrollmentIntent{}, false, ErrDisabled
	}
	return getEnrollmentIntentFrom(ctx, w.db, opID)
}

func getEnrollmentIntentFrom(ctx context.Context, q queryRower, opID string) (apikit.EnrollmentIntent, bool, error) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return apikit.EnrollmentIntent{}, false, nil
	}
	item, err := scanEnrollmentIntent(q.QueryRowContext(ctx, enrollmentSelectColumns+` WHERE i.operation_id = $1`, opID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.EnrollmentIntent{}, false, nil
	}
	if err != nil {
		return apikit.EnrollmentIntent{}, false, err
	}
	return item, true, nil
}

// ClaimEnrollmentFence atomically increments the per-operation execution fence
// and returns the authoritative enrollment operation.
func (w *Writer) ClaimEnrollmentFence(ctx context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.EnrollmentIntentFenceResponse{}, ErrDisabled
	}
	opID = strings.TrimSpace(opID)
	workerID = strings.TrimSpace(workerID)
	if opID == "" || workerID == "" {
		return apikit.EnrollmentIntentFenceResponse{}, fmt.Errorf("%w: operation_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM access_intent.enrollment_intent WHERE operation_id = $1 FOR SHARE`, opID).Scan(&exists); err != nil {
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO access_intent.enrollment_receipt (operation_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (operation_id) DO UPDATE SET
  fence_epoch = access_intent.enrollment_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, opID, workerID).Scan(&epoch); err != nil {
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	item, found, getErr := getEnrollmentIntentFrom(ctx, tx, opID)
	if getErr != nil {
		err = getErr
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.EnrollmentIntentFenceResponse{}, err
	}
	return apikit.EnrollmentIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordEnrollmentReceipt persists bounded enrollment execution progress
// conditional on operation fence epoch and desired revision. Activation must go
// through ActivateEnrollment so authority checks run atomically.
func (w *Writer) RecordEnrollmentReceipt(ctx context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.EnrollmentReceipt{}, ErrDisabled
	}
	opID = strings.TrimSpace(opID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.State = strings.TrimSpace(req.State)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.CertificateRequestName = strings.TrimSpace(req.CertificateRequestName)
	req.CertificateRequestUID = strings.TrimSpace(req.CertificateRequestUID)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if opID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validEnrollmentReceiptOutcome(req.Outcome) {
		return apikit.EnrollmentReceipt{}, fmt.Errorf("%w: operation_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}
	if req.State == apikit.EnrollmentStateActivated {
		return apikit.EnrollmentReceipt{}, fmt.Errorf("%w: use conditional activate endpoint to activate enrollment", ErrInvalidInput)
	}
	if req.State != "" && !validEnrollmentReceiptState(req.State) {
		return apikit.EnrollmentReceipt{}, fmt.Errorf("%w: invalid enrollment state", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.EnrollmentReceipt{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		currentRev   string
		currentState string
		crName       string
		currentEpoch int64
		appliedRev   string
		currentCRUID string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.desired_revision_id,
  i.state,
  i.certificate_request_name,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.certificate_request_uid, '')
FROM access_intent.enrollment_intent i
LEFT JOIN access_intent.enrollment_receipt r ON r.operation_id = i.operation_id
WHERE i.operation_id = $1
FOR UPDATE OF i`, opID).Scan(&currentRev, &currentState, &crName, &currentEpoch, &appliedRev, &currentCRUID)
	if err != nil {
		return apikit.EnrollmentReceipt{}, err
	}
	if currentEpoch != req.FenceEpoch || currentRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.EnrollmentReceipt{}, err
	}
	if currentState == apikit.EnrollmentStateActivated || currentState == apikit.EnrollmentStateRevoked {
		err = ErrStaleReceipt
		return apikit.EnrollmentReceipt{}, err
	}

	nextState := currentState
	if req.State != "" {
		nextState = req.State
	}
	nextCRName := crName
	if req.CertificateRequestName != "" {
		if req.CertificateRequestName != crName {
			err = fmt.Errorf("%w: certificate_request_name must match deterministic name %s", ErrInvalidInput, crName)
			return apikit.EnrollmentReceipt{}, err
		}
		nextCRName = req.CertificateRequestName
	}
	nextCRUID := currentCRUID
	if req.CertificateRequestUID != "" {
		nextCRUID = req.CertificateRequestUID
	}
	nextAppliedRev := appliedRev
	markAppliedAt := false
	if req.Outcome == apikit.AccessIntentOutcomeApplied {
		nextAppliedRev = req.DesiredRevisionID
		markAppliedAt = true
	}

	if nextState != currentState {
		if _, err = tx.ExecContext(ctx, `UPDATE access_intent.enrollment_intent SET state = $2, updated_at = clock_timestamp() WHERE operation_id = $1`, opID, nextState); err != nil {
			return apikit.EnrollmentReceipt{}, err
		}
	}

	var (
		out       apikit.EnrollmentReceipt
		appliedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
UPDATE access_intent.enrollment_receipt
SET
  applied_revision_id = $2,
  certificate_request_name = $3,
  certificate_request_uid = $4,
  outcome = $5,
  attempt = $6,
  worker_id = $7,
  last_error_class = $8,
  last_error = $9,
  applied_at = CASE WHEN $10 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE operation_id = $1 AND fence_epoch = $11
RETURNING fence_epoch, fence_holder, applied_revision_id, certificate_request_name, certificate_request_uid, outcome, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`,
		opID,
		nextAppliedRev,
		nextCRName,
		nextCRUID,
		req.Outcome,
		req.Attempt,
		req.WorkerID,
		req.LastErrorClass,
		req.LastError,
		markAppliedAt,
		req.FenceEpoch,
	).Scan(
		&out.FenceEpoch,
		&out.FenceHolder,
		&out.AppliedRevisionID,
		&out.CertificateRequestName,
		&out.CertificateRequestUID,
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
		return apikit.EnrollmentReceipt{}, err
	}
	if err != nil {
		return apikit.EnrollmentReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.EnrollmentReceipt{}, err
	}
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		out.AppliedAt = &t
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

// ActivateEnrollment atomically verifies that the target session is still
// active and non-revoked, the bound agent is still active, and the server UID
// still matches and is not retired before activating the enrollment.
// Lock order preserves session_sync_state -> agents -> enrollment_intent.
func (w *Writer) ActivateEnrollment(ctx context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error) {
	if w == nil || w.db == nil {
		return apikit.EnrollmentIntent{}, ErrDisabled
	}
	opID = strings.TrimSpace(opID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.CertificateRequestName = strings.TrimSpace(req.CertificateRequestName)
	req.CertificateRequestUID = strings.TrimSpace(req.CertificateRequestUID)
	if opID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" {
		return apikit.EnrollmentIntent{}, fmt.Errorf("%w: operation_id, desired_revision_id, positive fence_epoch, and worker_id are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Preserve platform lock ordering: session_sync_state -> agents -> enrollment_intent.
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).Scan(&seq); err != nil {
		return apikit.EnrollmentIntent{}, err
	}

	var (
		sessionUID   string
		serverUID    string
		agentID      string
		namespace    string
		sessionName  string
		currentRev   string
		currentState string
		crName       string
		currentEpoch int64
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.session_uid,
  i.server_uid,
  i.agent_id,
  i.namespace,
  i.session_name,
  i.desired_revision_id,
  i.state,
  i.certificate_request_name,
  COALESCE(r.fence_epoch, 0)
FROM access_intent.enrollment_intent i
LEFT JOIN access_intent.enrollment_receipt r ON r.operation_id = i.operation_id
WHERE i.operation_id = $1
FOR UPDATE OF i`, opID).Scan(&sessionUID, &serverUID, &agentID, &namespace, &sessionName, &currentRev, &currentState, &crName, &currentEpoch)
	if err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	if currentEpoch != req.FenceEpoch || currentRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.EnrollmentIntent{}, err
	}
	if currentState == apikit.EnrollmentStateRevoked || currentState == apikit.EnrollmentStateSuperseded {
		err = ErrAuthorityRevoked
		return apikit.EnrollmentIntent{}, err
	}
	if req.CertificateRequestName != "" && req.CertificateRequestName != crName {
		err = fmt.Errorf("%w: certificate_request_name mismatch", ErrInvalidInput)
		return apikit.EnrollmentIntent{}, err
	}

	revokeAndFail := func(reason string) (apikit.EnrollmentIntent, error) {
		if _, execErr := tx.ExecContext(ctx, `
UPDATE access_intent.enrollment_intent
SET state = $2, updated_at = clock_timestamp()
WHERE operation_id = $1`, opID, apikit.EnrollmentStateRevoked); execErr != nil {
			err = execErr
			return apikit.EnrollmentIntent{}, err
		}
		if _, execErr := tx.ExecContext(ctx, `
UPDATE access_intent.enrollment_receipt
SET
  certificate_request_name = $2,
  certificate_request_uid = COALESCE(NULLIF($3, ''), certificate_request_uid),
  outcome = $4,
  worker_id = $5,
  last_error_class = $6,
  last_error = $7,
  updated_at = clock_timestamp()
WHERE operation_id = $1 AND fence_epoch = $8`,
			opID, crName, req.CertificateRequestUID, apikit.AccessIntentOutcomeTerminal,
			req.WorkerID, apikit.AccessIntentErrorAuthorityRevoked, truncateSanitized(reason, maxStoredErrorLen), req.FenceEpoch,
		); execErr != nil {
			err = execErr
			return apikit.EnrollmentIntent{}, err
		}
		if commitErr := tx.Commit(); commitErr != nil {
			err = commitErr
			return apikit.EnrollmentIntent{}, err
		}
		return apikit.EnrollmentIntent{}, fmt.Errorf("%w: %s", ErrAuthorityRevoked, reason)
	}

	var retired bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_retired_servers WHERE server_uid = $1)`, serverUID).Scan(&retired); err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	if retired {
		return revokeAndFail("target server is retired")
	}

	var sessionData []byte
	scanErr := tx.QueryRowContext(ctx, `SELECT data FROM sessions WHERE namespace = $1 AND name = $2`, namespace, sessionName).Scan(&sessionData)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return revokeAndFail("session no longer exists")
	}
	if scanErr != nil {
		err = scanErr
		return apikit.EnrollmentIntent{}, err
	}
	var sess sessionstate.Session
	if err = json.Unmarshal(sessionData, &sess); err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	if sess.UID != sessionUID {
		return revokeAndFail("session UID no longer matches enrollment")
	}
	if sess.ServerUID != serverUID {
		return revokeAndFail("server UID no longer matches enrollment")
	}
	if sess.Binding.Revoked {
		return revokeAndFail("session is revoked")
	}
	expires, parseErr := time.Parse(time.RFC3339, sess.Binding.ExpiresAt)
	if parseErr != nil || !expires.After(time.Now()) {
		return revokeAndFail("session is expired")
	}

	effectiveAgentID := agentID
	if effectiveAgentID == "" {
		effectiveAgentID = string(sess.Binding.AgentID)
	} else if sess.Binding.AgentID != "" && string(sess.Binding.AgentID) != effectiveAgentID {
		return revokeAndFail("agent ID no longer matches session")
	}
	if effectiveAgentID != "" {
		var agentStatus, agentTeam string
		agentErr := tx.QueryRowContext(ctx, `SELECT status, team_id::text FROM agents WHERE id = $1 FOR UPDATE`, effectiveAgentID).Scan(&agentStatus, &agentTeam)
		if errors.Is(agentErr, sql.ErrNoRows) {
			return revokeAndFail("bound agent no longer exists")
		}
		if agentErr != nil {
			err = agentErr
			return apikit.EnrollmentIntent{}, err
		}
		if agentStatus != "active" || (sess.Binding.TeamID != "" && agentTeam != string(sess.Binding.TeamID)) {
			return revokeAndFail("bound agent is inactive or team mismatched")
		}
	}

	if sess.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] == "true" {
		delete(sess.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
		updatedSessionData, marshalErr := json.Marshal(sess)
		if marshalErr != nil {
			err = marshalErr
			return apikit.EnrollmentIntent{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data = $2 WHERE id = $1`, sess.ID, string(updatedSessionData)); err != nil {
			return apikit.EnrollmentIntent{}, err
		}
		var nextSeq int64
		if err = tx.QueryRowContext(ctx, `UPDATE session_sync_state SET sequence = sequence + 1 WHERE singleton RETURNING sequence`).Scan(&nextSeq); err != nil {
			return apikit.EnrollmentIntent{}, err
		}
		changeData, marshalErr := json.Marshal(sessionstate.Change{
			Kind:      "session",
			Sequence:  nextSeq,
			Operation: "upsert",
			ID:        sess.ID,
			Session:   &sess,
		})
		if marshalErr != nil {
			err = marshalErr
			return apikit.EnrollmentIntent{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO session_changes(sequence, server_namespace, server_uid, data) VALUES($1, $2, $3, $4)`,
			nextSeq, sess.ServerNamespace, sess.ServerUID, string(changeData)); err != nil {
			return apikit.EnrollmentIntent{}, err
		}
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE access_intent.enrollment_intent
SET
  state = $2,
  activated_at = COALESCE(activated_at, clock_timestamp()),
  updated_at = clock_timestamp()
WHERE operation_id = $1`, opID, apikit.EnrollmentStateActivated); err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE access_intent.enrollment_receipt
SET
  applied_revision_id = $2,
  certificate_request_name = $3,
  certificate_request_uid = COALESCE(NULLIF($4, ''), certificate_request_uid),
  outcome = $5,
  worker_id = $6,
  last_error_class = '',
  last_error = '',
  applied_at = clock_timestamp(),
  updated_at = clock_timestamp()
WHERE operation_id = $1 AND fence_epoch = $7`,
		opID, req.DesiredRevisionID, crName, req.CertificateRequestUID,
		apikit.AccessIntentOutcomeActivated, req.WorkerID, req.FenceEpoch); err != nil {
		return apikit.EnrollmentIntent{}, err
	}

	item, found, getErr := getEnrollmentIntentFrom(ctx, tx, opID)
	if getErr != nil {
		err = getErr
		return apikit.EnrollmentIntent{}, err
	}
	if !found {
		err = sql.ErrNoRows
		return apikit.EnrollmentIntent{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.EnrollmentIntent{}, err
	}
	return item, nil
}

func validEnrollmentReceiptOutcome(v string) bool {
	switch v {
	case apikit.AccessIntentOutcomeApplied,
		apikit.AccessIntentOutcomeSuperseded,
		apikit.AccessIntentOutcomeFailed,
		apikit.AccessIntentOutcomeTerminal:
		return true
	default:
		return false
	}
}

func validEnrollmentReceiptState(v string) bool {
	switch v {
	case apikit.EnrollmentStatePending,
		apikit.EnrollmentStateSubmitted,
		apikit.EnrollmentStateIssued,
		apikit.EnrollmentStateRevoked,
		apikit.EnrollmentStateFailed,
		apikit.EnrollmentStateSuperseded:
		return true
	default:
		return false
	}
}
