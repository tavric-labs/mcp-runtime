package teamintent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"mcp-runtime/pkg/apikit"
)

var (
	// ErrDisabled is returned when PLATFORM_TEAM_INTENT_JOURNAL is not enabled.
	ErrDisabled = errors.New("team intent journal is disabled")
	// ErrStaleReceipt is returned when a worker receipt fails fence, revision,
	// or incarnation ownership validation.
	ErrStaleReceipt = errors.New("stale team intent receipt")
	// ErrStaleObservation is returned when an observer epoch, revision, or UID
	// fails conditional validation.
	ErrStaleObservation = errors.New("stale team intent observation")
	// ErrInvalidInput is returned when a team intent API request is malformed.
	ErrInvalidInput = errors.New("invalid team intent request")
)

const (
	defaultClusterKey = "default"
	maxStoredErrorLen = 1024
)

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const intentSelectColumns = `
SELECT
  i.team_id,
  COALESCE(t.slug, ''),
  COALESCE(t.display_name, ''),
  COALESCE(n.namespace, CASE WHEN COALESCE(t.slug, '') <> '' THEN 'mcp-team-' || t.slug ELSE '' END),
  i.revision_id,
  i.deleted,
  i.updated_at,
  COALESCE((
    SELECT t2.id::text
    FROM teams t2
    WHERE t2.slug = t.slug AND t2.deleted_at IS NULL
    LIMIT 1
  ), ''),
  COALESCE((
    SELECT ARRAY_AGG(t3.id::text ORDER BY t3.created_at ASC, t3.id ASC)
    FROM teams t3
    WHERE t3.slug = t.slug AND t3.deleted_at IS NOT NULL AND t3.id <> t.id AND t3.created_at <= t.created_at
  ), ARRAY[]::text[]),
  r.team_id IS NOT NULL,
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.fence_holder, ''),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.outcome, ''),
  COALESCE(r.namespace_uid, ''),
  COALESCE(r.attempt, 0),
  COALESCE(r.worker_id, ''),
  COALESCE(r.last_error_class, ''),
  COALESCE(r.last_error, ''),
  r.applied_at,
  r.updated_at,
  o.team_id IS NOT NULL,
  COALESCE(o.observer_id, ''),
  COALESCE(o.observer_epoch, 0),
  COALESCE(o.observed_revision_id, ''),
  COALESCE(o.namespace_uid, ''),
  COALESCE(o.health, ''),
  COALESCE(o.reason, ''),
  COALESCE(o.message, ''),
  COALESCE(o.conditions, '[]'::jsonb),
  o.last_transition_at,
  o.last_verified_at,
  o.updated_at
FROM team_intent.team_namespace_intent i
LEFT JOIN teams t ON t.id::text = i.team_id
LEFT JOIN LATERAL (
  SELECT ns.namespace
  FROM namespaces ns
  WHERE ns.team_id = t.id AND COALESCE(ns.scope, 'team') = 'team'
  ORDER BY ns.created_at DESC
  LIMIT 1
) n ON true
LEFT JOIN team_intent.team_namespace_receipt r ON r.team_id = i.team_id
LEFT JOIN team_intent.team_namespace_observation o ON o.team_id = i.team_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTeamIntent(scanner rowScanner) (apikit.TeamNamespaceIntent, error) {
	var (
		item            apikit.TeamNamespaceIntent
		priorIDs        pq.StringArray
		hasReceipt      bool
		receipt         apikit.TeamNamespaceReceipt
		receiptApplied  sql.NullTime
		receiptUpdated  sql.NullTime
		hasObs          bool
		obs             apikit.TeamNamespaceObservation
		obsCondsJSON    []byte
		obsTransitionAt sql.NullTime
		obsVerifiedAt   sql.NullTime
		obsUpdatedAt    sql.NullTime
	)
	if err := scanner.Scan(
		&item.TeamID,
		&item.Slug,
		&item.Name,
		&item.Namespace,
		&item.DesiredRevisionID,
		&item.Deleted,
		&item.UpdatedAt,
		&item.ActiveOwnerTeamID,
		&priorIDs,
		&hasReceipt,
		&receipt.FenceEpoch,
		&receipt.FenceHolder,
		&receipt.AppliedRevisionID,
		&receipt.Outcome,
		&receipt.NamespaceUID,
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
		&obs.NamespaceUID,
		&obs.Health,
		&obs.Reason,
		&obs.Message,
		&obsCondsJSON,
		&obsTransitionAt,
		&obsVerifiedAt,
		&obsUpdatedAt,
	); err != nil {
		return apikit.TeamNamespaceIntent{}, err
	}
	if len(priorIDs) > 0 {
		item.PriorDeletedTeamIDs = []string(priorIDs)
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
	if hasObs {
		if len(obsCondsJSON) > 0 {
			_ = json.Unmarshal(obsCondsJSON, &obs.Conditions)
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
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

// ListIntents returns all recorded team namespace intents with their latest
// receipts and independent observations.
func (w *Writer) ListIntents(ctx context.Context) ([]apikit.TeamNamespaceIntent, error) {
	if w == nil || w.db == nil {
		return nil, ErrDisabled
	}
	rows, err := w.db.QueryContext(ctx, intentSelectColumns+` ORDER BY i.updated_at DESC, i.team_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]apikit.TeamNamespaceIntent, 0)
	for rows.Next() {
		item, err := scanTeamIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetIntent returns the authoritative desired intent for one team incarnation.
func (w *Writer) GetIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	if w == nil || w.db == nil {
		return apikit.TeamNamespaceIntent{}, false, ErrDisabled
	}
	return getIntentFrom(ctx, w.db, teamID)
}

func getIntentFrom(ctx context.Context, q queryRower, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return apikit.TeamNamespaceIntent{}, false, nil
	}
	item, err := scanTeamIntent(q.QueryRowContext(ctx, intentSelectColumns+` WHERE i.team_id = $1`, teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.TeamNamespaceIntent{}, false, nil
	}
	if err != nil {
		return apikit.TeamNamespaceIntent{}, false, err
	}
	return item, true, nil
}

// ClaimFence atomically bumps the per-team execution fence epoch and returns
// the authoritative current desired intent.
func (w *Writer) ClaimFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error) {
	if w == nil || w.db == nil {
		return apikit.TeamIntentFenceResponse{}, ErrDisabled
	}
	teamID = strings.TrimSpace(teamID)
	workerID = strings.TrimSpace(workerID)
	if teamID == "" || workerID == "" {
		return apikit.TeamIntentFenceResponse{}, fmt.Errorf("%w: team_id and worker_id are required", ErrInvalidInput)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT team_id FROM team_intent.team_namespace_intent WHERE team_id = $1 FOR SHARE`, teamID).Scan(&exists); err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO team_intent.team_namespace_receipt (team_id, fence_epoch, fence_holder, updated_at)
VALUES ($1, 1, $2, clock_timestamp())
ON CONFLICT (team_id) DO UPDATE SET
  fence_epoch = team_intent.team_namespace_receipt.fence_epoch + 1,
  fence_holder = EXCLUDED.fence_holder,
  updated_at = EXCLUDED.updated_at
RETURNING fence_epoch`, teamID, workerID).Scan(&epoch); err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	item, ok, err := getIntentFrom(ctx, tx, teamID)
	if err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	if !ok {
		err = sql.ErrNoRows
		return apikit.TeamIntentFenceResponse{}, err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE team_intent.operation SET state='Running', updated_at=clock_timestamp()
WHERE team_id=$1 AND revision_id=$2 AND state='Pending'`, teamID, item.DesiredRevisionID); err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.TeamIntentFenceResponse{}, err
	}
	return apikit.TeamIntentFenceResponse{FenceEpoch: epoch, Intent: item}, nil
}

// RecordReceipt stores a worker execution receipt conditional on the team
// incarnation, current desired revision, and execution fence epoch.
func (w *Writer) RecordReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error) {
	if w == nil || w.db == nil {
		return apikit.TeamNamespaceReceipt{}, ErrDisabled
	}
	teamID = strings.TrimSpace(teamID)
	req.DesiredRevisionID = strings.TrimSpace(req.DesiredRevisionID)
	req.Outcome = strings.TrimSpace(req.Outcome)
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.NamespaceUID = strings.TrimSpace(req.NamespaceUID)
	req.LastErrorClass = strings.TrimSpace(req.LastErrorClass)
	req.LastError = truncateSanitized(req.LastError, maxStoredErrorLen)
	if teamID == "" || req.DesiredRevisionID == "" || req.FenceEpoch <= 0 || req.WorkerID == "" || !validOutcome(req.Outcome) {
		return apikit.TeamNamespaceReceipt{}, fmt.Errorf("%w: team_id, desired_revision_id, positive fence_epoch, worker_id, and valid outcome are required", ErrInvalidInput)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.TeamNamespaceReceipt{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		currentRev   string
		deleted      bool
		slug         string
		currentEpoch int64
		appliedRev   string
		currentNSUID string
		currentAppAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.revision_id,
  i.deleted,
  COALESCE(t.slug, ''),
  COALESCE(r.fence_epoch, 0),
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.namespace_uid, ''),
  r.applied_at
FROM team_intent.team_namespace_intent i
LEFT JOIN teams t ON t.id::text = i.team_id
LEFT JOIN team_intent.team_namespace_receipt r ON r.team_id = i.team_id
WHERE i.team_id = $1
FOR UPDATE OF i`, teamID).Scan(&currentRev, &deleted, &slug, &currentEpoch, &appliedRev, &currentNSUID, &currentAppAt)
	if err != nil {
		return apikit.TeamNamespaceReceipt{}, err
	}
	if currentEpoch != req.FenceEpoch || currentRev != req.DesiredRevisionID {
		err = ErrStaleReceipt
		return apikit.TeamNamespaceReceipt{}, err
	}

	var activeOwner string
	if slug != "" {
		if scanErr := tx.QueryRowContext(ctx, `SELECT id::text FROM teams WHERE slug = $1 AND deleted_at IS NULL LIMIT 1`, slug).Scan(&activeOwner); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			err = scanErr
			return apikit.TeamNamespaceReceipt{}, err
		}
	}

	nextAppliedRev := appliedRev
	nextNSUID := currentNSUID
	nextErrClass := req.LastErrorClass
	nextErr := req.LastError
	markAppliedAt := false

	switch req.Outcome {
	case apikit.TeamIntentOutcomeApplied:
		if deleted || activeOwner != teamID {
			err = ErrStaleReceipt
			return apikit.TeamNamespaceReceipt{}, err
		}
		nextAppliedRev = req.DesiredRevisionID
		nextNSUID = req.NamespaceUID
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.TeamIntentOutcomeCleaned:
		if !deleted || activeOwner != "" {
			err = ErrStaleReceipt
			return apikit.TeamNamespaceReceipt{}, err
		}
		nextAppliedRev = req.DesiredRevisionID
		nextErrClass = ""
		nextErr = ""
		markAppliedAt = true
	case apikit.TeamIntentOutcomeSuperseded:
		if !deleted && activeOwner == teamID {
			err = ErrStaleReceipt
			return apikit.TeamNamespaceReceipt{}, err
		}
		nextErrClass = ""
		nextErr = ""
	case apikit.TeamIntentOutcomeFailed, apikit.TeamIntentOutcomeTerminal:
		// Preserve prior applied_revision_id; surface failure without fabricating success.
	}

	var (
		out       apikit.TeamNamespaceReceipt
		appliedAt sql.NullTime
	)
	query := `
UPDATE team_intent.team_namespace_receipt
SET
  applied_revision_id = $2,
  outcome = $3,
  namespace_uid = $4,
  attempt = $5,
  worker_id = $6,
  last_error_class = $7,
  last_error = $8,
  applied_at = CASE WHEN $9 THEN clock_timestamp() ELSE applied_at END,
  updated_at = clock_timestamp()
WHERE team_id = $1 AND fence_epoch = $10
RETURNING fence_epoch, fence_holder, applied_revision_id, outcome, namespace_uid, attempt, worker_id, last_error_class, last_error, applied_at, updated_at`
	err = tx.QueryRowContext(ctx, query,
		teamID,
		nextAppliedRev,
		req.Outcome,
		nextNSUID,
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
		&out.NamespaceUID,
		&out.Attempt,
		&out.WorkerID,
		&out.LastErrorClass,
		&out.LastError,
		&appliedAt,
		&out.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrStaleReceipt
		return apikit.TeamNamespaceReceipt{}, err
	}
	if err != nil {
		return apikit.TeamNamespaceReceipt{}, err
	}
	if err = updateOperationFromReceipt(ctx, tx, teamID, req); err != nil {
		return apikit.TeamNamespaceReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.TeamNamespaceReceipt{}, err
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
func (w *Writer) ClaimObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error) {
	if w == nil || w.db == nil {
		return apikit.TeamIntentObserverLeaseResponse{}, ErrDisabled
	}
	clusterKey = strings.TrimSpace(clusterKey)
	if clusterKey == "" {
		clusterKey = defaultClusterKey
	}
	observerID = strings.TrimSpace(observerID)
	if observerID == "" {
		return apikit.TeamIntentObserverLeaseResponse{}, fmt.Errorf("%w: observer_id is required", ErrInvalidInput)
	}
	var epoch int64
	err := w.db.QueryRowContext(ctx, `
INSERT INTO team_intent.observer_lease (cluster_key, observer_id, observer_epoch, updated_at)
VALUES ($1, $2, 1, clock_timestamp())
ON CONFLICT (cluster_key) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = team_intent.observer_lease.observer_epoch + 1,
  updated_at = EXCLUDED.updated_at
RETURNING observer_epoch`, clusterKey, observerID).Scan(&epoch)
	if err != nil {
		return apikit.TeamIntentObserverLeaseResponse{}, err
	}
	return apikit.TeamIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: epoch,
	}, nil
}

// RecordObservation persists independent Kubernetes health observations for a
// team incarnation without modifying desired intent, receipts, or River jobs.
func (w *Writer) RecordObservation(ctx context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error) {
	if w == nil || w.db == nil {
		return apikit.TeamNamespaceObservation{}, ErrDisabled
	}
	teamID = strings.TrimSpace(teamID)
	req.ClusterKey = strings.TrimSpace(req.ClusterKey)
	if req.ClusterKey == "" {
		req.ClusterKey = defaultClusterKey
	}
	req.ObserverID = strings.TrimSpace(req.ObserverID)
	req.ObservedRevisionID = strings.TrimSpace(req.ObservedRevisionID)
	req.NamespaceUID = strings.TrimSpace(req.NamespaceUID)
	req.Health = strings.TrimSpace(req.Health)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Message = truncateSanitized(req.Message, maxStoredErrorLen)
	if teamID == "" || req.ObserverID == "" || req.ObserverEpoch <= 0 || !validHealth(req.Health) {
		return apikit.TeamNamespaceObservation{}, fmt.Errorf("%w: team_id, observer_id, positive observer_epoch, and valid health are required", ErrInvalidInput)
	}
	verifiedAt := req.VerifiedAt.UTC()
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	conds := req.Conditions
	if conds == nil {
		conds = []apikit.TeamIntentCondition{}
	}
	condsJSON, err := json.Marshal(conds)
	if err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		desiredRev   string
		deleted      bool
		appliedRev   string
		receiptNSUID string
	)
	err = tx.QueryRowContext(ctx, `
SELECT
  i.revision_id,
  i.deleted,
  COALESCE(r.applied_revision_id, ''),
  COALESCE(r.namespace_uid, '')
FROM team_intent.team_namespace_intent i
LEFT JOIN team_intent.team_namespace_receipt r ON r.team_id = i.team_id
WHERE i.team_id = $1
FOR SHARE OF i`, teamID).Scan(&desiredRev, &deleted, &appliedRev, &receiptNSUID)
	if err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}

	var leaseEpoch int64
	if scanErr := tx.QueryRowContext(ctx, `SELECT observer_epoch FROM team_intent.observer_lease WHERE cluster_key = $1`, req.ClusterKey).Scan(&leaseEpoch); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.TeamNamespaceObservation{}, err
	}
	if leaseEpoch > 0 && req.ObserverEpoch < leaseEpoch {
		err = ErrStaleObservation
		return apikit.TeamNamespaceObservation{}, err
	}

	var (
		existingEpoch      int64
		existingHealth     string
		existingReason     string
		existingTransition sql.NullTime
	)
	scanErr := tx.QueryRowContext(ctx, `
SELECT observer_epoch, health, reason, last_transition_at
FROM team_intent.team_namespace_observation
WHERE team_id = $1
FOR UPDATE`, teamID).Scan(&existingEpoch, &existingHealth, &existingReason, &existingTransition)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		err = scanErr
		return apikit.TeamNamespaceObservation{}, err
	}
	if existingEpoch > 0 && req.ObserverEpoch < existingEpoch {
		err = ErrStaleObservation
		return apikit.TeamNamespaceObservation{}, err
	}

	// Reject observations for obsolete revisions when newer intent/receipt exists.
	if req.ObservedRevisionID != "" && req.ObservedRevisionID != desiredRev && req.ObservedRevisionID != appliedRev {
		err = ErrStaleObservation
		return apikit.TeamNamespaceObservation{}, err
	}
	// Ready requires an active incarnation with an applied current revision and matching UID.
	if req.Health == apikit.TeamIntentHealthReady {
		if deleted || appliedRev == "" || appliedRev != desiredRev || req.ObservedRevisionID != appliedRev {
			err = ErrStaleObservation
			return apikit.TeamNamespaceObservation{}, err
		}
		if receiptNSUID != "" && req.NamespaceUID != receiptNSUID {
			err = ErrStaleObservation
			return apikit.TeamNamespaceObservation{}, err
		}
	}

	transitionAt := verifiedAt
	if existingTransition.Valid && existingHealth == req.Health && existingReason == req.Reason {
		transitionAt = existingTransition.Time.UTC()
	}

	var (
		out      apikit.TeamNamespaceObservation
		outConds []byte
	)
	err = tx.QueryRowContext(ctx, `
INSERT INTO team_intent.team_namespace_observation (
  team_id, observer_id, observer_epoch, observed_revision_id, namespace_uid,
  health, reason, message, conditions, last_transition_at, last_verified_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, clock_timestamp())
ON CONFLICT (team_id) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = EXCLUDED.observer_epoch,
  observed_revision_id = EXCLUDED.observed_revision_id,
  namespace_uid = EXCLUDED.namespace_uid,
  health = EXCLUDED.health,
  reason = EXCLUDED.reason,
  message = EXCLUDED.message,
  conditions = EXCLUDED.conditions,
  last_transition_at = EXCLUDED.last_transition_at,
  last_verified_at = EXCLUDED.last_verified_at,
  updated_at = EXCLUDED.updated_at
RETURNING observer_id, observer_epoch, observed_revision_id, namespace_uid, health, reason, message, conditions, last_transition_at, last_verified_at, updated_at`,
		teamID,
		req.ObserverID,
		req.ObserverEpoch,
		req.ObservedRevisionID,
		req.NamespaceUID,
		req.Health,
		req.Reason,
		req.Message,
		string(condsJSON),
		transitionAt,
		verifiedAt,
	).Scan(
		&out.ObserverID,
		&out.ObserverEpoch,
		&out.ObservedRevisionID,
		&out.NamespaceUID,
		&out.Health,
		&out.Reason,
		&out.Message,
		&outConds,
		&out.LastTransitionAt,
		&out.LastVerifiedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}
	if err = tx.Commit(); err != nil {
		return apikit.TeamNamespaceObservation{}, err
	}
	if len(outConds) > 0 {
		_ = json.Unmarshal(outConds, &out.Conditions)
	}
	out.LastTransitionAt = out.LastTransitionAt.UTC()
	out.LastVerifiedAt = out.LastVerifiedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func validOutcome(v string) bool {
	switch v {
	case apikit.TeamIntentOutcomeApplied,
		apikit.TeamIntentOutcomeCleaned,
		apikit.TeamIntentOutcomeSuperseded,
		apikit.TeamIntentOutcomeFailed,
		apikit.TeamIntentOutcomeTerminal:
		return true
	default:
		return false
	}
}

func validHealth(v string) bool {
	switch v {
	case apikit.TeamIntentHealthPending,
		apikit.TeamIntentHealthProgressing,
		apikit.TeamIntentHealthReady,
		apikit.TeamIntentHealthDegraded,
		apikit.TeamIntentHealthDeleted,
		apikit.TeamIntentHealthUnknown:
		return true
	default:
		return false
	}
}

func truncateSanitized(raw string, maxLen int) string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r", " "), "\n", " "))
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
