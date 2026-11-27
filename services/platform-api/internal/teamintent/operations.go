package teamintent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
)

// ErrIdempotencyConflict rejects reusing an actor/action replay key for
// different normalized input without changing the original acceptance.
var ErrIdempotencyConflict = errors.New("idempotency key already accepted different input")

// OperationAcceptance includes the immutable accepted team representation.
// Actor and replay metadata stay in storage and are not part of the wire shape.
type OperationAcceptance struct {
	Team      apikit.Team
	Operation apikit.Operation
}

// OperationRequest binds immutable accepted contents to the authenticated
// request's replay scope and the revision enqueued in the same transaction.
type OperationRequest struct {
	ID             string
	RevisionID     string
	ActorID        string
	Kind           string
	IdempotencyKey string
	RequestHash    string
	Team           apikit.Team
}

type operationRecord struct {
	OperationAcceptance
	requestHash string
}

const operationSelect = `
SELECT operation_id, kind, team_id, revision_id, team_snapshot, state,
       error_class, request_id, created_at, updated_at, request_hash
FROM team_intent.operation`

func scanOperation(row rowScanner) (operationRecord, error) {
	var record operationRecord
	var snapshot []byte
	op := &record.Operation
	if err := row.Scan(&op.ID, &op.Kind, &op.ResourceID, &op.DesiredRevisionID, &snapshot,
		&op.State, &op.ErrorClass, &op.RequestID, &op.CreatedAt, &op.UpdatedAt, &record.requestHash); err != nil {
		return operationRecord{}, err
	}
	if err := json.Unmarshal(snapshot, &record.Team); err != nil {
		return operationRecord{}, fmt.Errorf("decode accepted team snapshot: %w", err)
	}
	op.Namespace = record.Team.Namespace
	op.CreatedAt, op.UpdatedAt = op.CreatedAt.UTC(), op.UpdatedAt.UTC()
	record.Team.CreatedAt = record.Team.CreatedAt.UTC()
	return record, nil
}

func validOperationKey(key string, limit int) bool {
	if key == "" || len(key) > limit {
		return false
	}
	for _, ch := range key {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

func replayKeyHash(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

// LookupAcceptance serializes only requests for the same actor/action/key for
// the lifetime of tx. Hash collisions can serialize unrelated requests but
// cannot make one actor observe another actor's acceptance.
func (w *Writer) LookupAcceptance(ctx context.Context, tx *sql.Tx, actorID, kind, key, requestHash string) (OperationAcceptance, bool, error) {
	if !validOperationKey(actorID, 256) || !validOperationKey(key, 128) || kind != apikit.OperationTeamCreate {
		return OperationAcceptance{}, false, fmt.Errorf("%w: actor, supported action, and replay key are required", ErrInvalidInput)
	}
	keyHash := replayKeyHash(key)
	scope, _ := json.Marshal([]string{actorID, kind, keyHash})
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 873))`, string(scope)); err != nil {
		return OperationAcceptance{}, false, err
	}
	record, err := scanOperation(tx.QueryRowContext(ctx, operationSelect+`
WHERE actor_id = $1 AND kind = $2 AND idempotency_hash = $3`, actorID, kind, keyHash))
	if errors.Is(err, sql.ErrNoRows) {
		return OperationAcceptance{}, false, nil
	}
	if err != nil {
		return OperationAcceptance{}, false, err
	}
	if record.requestHash != requestHash {
		return OperationAcceptance{}, false, ErrIdempotencyConflict
	}
	return record.OperationAcceptance, true, nil
}

// RecordAcceptance persists the immutable accepted contents and their operation
// in the caller's domain/intent/River transaction. Only lifecycle columns change
// later; receipts never replace this snapshot with current resource contents.
func (w *Writer) RecordAcceptance(ctx context.Context, tx *sql.Tx, request OperationRequest) (OperationAcceptance, error) {
	snapshot, err := json.Marshal(request.Team)
	if err != nil {
		return OperationAcceptance{}, err
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO team_intent.operation (
 operation_id, kind, actor_id, idempotency_hash, request_hash, team_id, revision_id,
 team_snapshot, state, request_id, traceparent, tracestate, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'Pending',$9,$10,$11,clock_timestamp(),clock_timestamp())`,
		request.ID, request.Kind, request.ActorID, replayKeyHash(request.IdempotencyKey), request.RequestHash, request.Team.ID, request.RevisionID,
		string(snapshot), httpx.RequestID(ctx), carrier.Get("traceparent"), carrier.Get("tracestate")); err != nil {
		return OperationAcceptance{}, err
	}
	record, err := scanOperation(tx.QueryRowContext(ctx, operationSelect+` WHERE operation_id = $1`, request.ID))
	return record.OperationAcceptance, err
}

// GetOperation is actor-scoped even when an operation UUID is guessed or copied.
func (w *Writer) GetOperation(ctx context.Context, operationID, actorID string) (OperationAcceptance, bool, error) {
	if w == nil || w.db == nil {
		return OperationAcceptance{}, false, ErrDisabled
	}
	if !validOperationKey(actorID, 256) {
		return OperationAcceptance{}, false, fmt.Errorf("%w: authenticated actor is required", ErrInvalidInput)
	}
	record, err := scanOperation(w.db.QueryRowContext(ctx, operationSelect+`
WHERE operation_id = $1 AND actor_id = $2`, strings.TrimSpace(operationID), actorID))
	if errors.Is(err, sql.ErrNoRows) {
		return OperationAcceptance{}, false, nil
	}
	return record.OperationAcceptance, err == nil, err
}

func updateOperationFromReceipt(ctx context.Context, tx *sql.Tx, teamID string, req apikit.TeamIntentReceiptRequest) error {
	state := apikit.OperationRunning
	errClass := ""
	switch req.Outcome {
	case apikit.TeamIntentOutcomeApplied, apikit.TeamIntentOutcomeCleaned:
		state = apikit.OperationSucceeded
	case apikit.TeamIntentOutcomeSuperseded:
		state = apikit.OperationSuperseded
	case apikit.TeamIntentOutcomeTerminal:
		state = apikit.OperationFailed
	case apikit.TeamIntentOutcomeFailed:
		if req.LastErrorClass == apikit.TeamIntentErrorExhausted {
			state = apikit.OperationFailed
		}
	}
	if req.Outcome == apikit.TeamIntentOutcomeFailed || req.Outcome == apikit.TeamIntentOutcomeTerminal {
		switch req.LastErrorClass {
		case apikit.TeamIntentErrorTransient, apikit.TeamIntentErrorOwnershipConflict, apikit.TeamIntentErrorInvalidConfig, apikit.TeamIntentErrorExhausted:
			errClass = req.LastErrorClass
		default:
			errClass = apikit.TeamIntentErrorTransient
		}
	}
	_, err := tx.ExecContext(ctx, `
UPDATE team_intent.operation SET state=$3, error_class=$4, updated_at=clock_timestamp()
WHERE team_id=$1 AND revision_id=$2 AND state IN ('Pending','Running')`,
		teamID, req.DesiredRevisionID, state, errClass)
	return err
}
