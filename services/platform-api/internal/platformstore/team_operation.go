package platformstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
)

// TeamCreateAcceptanceRequest contains normalized public team-create input and
// the authenticated actor's replay key. ActorID is supplied by the server.
type TeamCreateAcceptanceRequest struct {
	Slug            string
	Name            string
	CreatedByUserID string
	ActorID         string
	IdempotencyKey  string
}

// TeamDeleteAcceptanceRequest scopes deletion retries to their original actor
// and acceptance, even if the slug is later reused by another team incarnation.
type TeamDeleteAcceptanceRequest struct {
	Slug           string
	ActorID        string
	IdempotencyKey string
}

// AcceptTeamDelete atomically tombstones the directory rows and queues watch
// cleanup. The namespace and its data are retained by the runtime reconciler.
func (s *Store) AcceptTeamDelete(ctx context.Context, request TeamDeleteAcceptanceRequest) (TeamOperationAcceptance, error) {
	if s == nil || s.teamIntents == nil {
		return TeamOperationAcceptance{}, teamintent.ErrDisabled
	}
	slug := NormalizeTeamSlug(request.Slug)
	if err := ValidateTeamSlug(slug); err != nil {
		return TeamOperationAcceptance{}, err
	}
	digest := sha256.Sum256([]byte(slug))
	requestHash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := s.teamIntents.LookupAcceptance(ctx, tx, request.ActorID, apikit.OperationTeamDelete, request.IdempotencyKey, requestHash)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	if found {
		return TeamOperationAcceptance{Team: Team(existing.Team), Operation: existing.Operation, Replayed: true}, nil
	}
	var team Team
	if err := tx.QueryRowContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), t.created_at
FROM teams t LEFT JOIN namespaces n ON n.team_id=t.id AND n.deleted_at IS NULL AND COALESCE(n.scope,'team')='team'
WHERE t.slug=$1 AND t.deleted_at IS NULL`, slug).Scan(&team.ID, &team.Slug, &team.Name, &team.Namespace, &team.CreatedAt); err != nil {
		return TeamOperationAcceptance{}, err
	}
	team.CreatedAt = team.CreatedAt.UTC()
	deleted, err := deleteTeamRows(ctx, tx, team.ID)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	affected, err := deleted.RowsAffected()
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	if affected == 0 {
		return TeamOperationAcceptance{}, sql.ErrNoRows
	}
	operationID := uuid.NewString()
	revisionID, err := s.teamIntents.RecordWithOperation(ctx, tx, team.ID, true, operationID)
	if err != nil {
		return TeamOperationAcceptance{}, fmt.Errorf("record accepted team deletion: %w", err)
	}
	accepted, err := s.teamIntents.RecordAcceptance(ctx, tx, teamintent.OperationRequest{
		ID: operationID, RevisionID: revisionID, ActorID: request.ActorID,
		Kind: apikit.OperationTeamDelete, IdempotencyKey: request.IdempotencyKey,
		RequestHash: requestHash, Team: apikit.Team(team),
	})
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamOperationAcceptance{}, err
	}
	return TeamOperationAcceptance{Team: Team(accepted.Team), Operation: accepted.Operation}, nil
}

// TeamOperationAcceptance preserves the accepted resource and operation across
// response loss. Current resource health is read independently.
type TeamOperationAcceptance struct {
	Team      Team             `json:"team"`
	Operation apikit.Operation `json:"operation"`
	Replayed  bool             `json:"-"`
}

// AcceptTeamCreate commits a team, owner membership, namespace intent, operation,
// and River job together. A replay never changes the accepted contents or IDs.
func (s *Store) AcceptTeamCreate(ctx context.Context, request TeamCreateAcceptanceRequest) (TeamOperationAcceptance, error) {
	if s == nil || s.teamIntents == nil {
		return TeamOperationAcceptance{}, teamintent.ErrDisabled
	}
	team, err := newTeamRecord(request.Slug, request.Name)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	request.CreatedByUserID = strings.TrimSpace(request.CreatedByUserID)
	payload, _ := json.Marshal(struct{ Slug, Name, CreatedBy string }{team.Slug, team.Name, request.CreatedByUserID})
	digest := sha256.Sum256(payload)
	requestHash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := s.teamIntents.LookupAcceptance(ctx, tx, request.ActorID, apikit.OperationTeamCreate, request.IdempotencyKey, requestHash)
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	if found {
		return TeamOperationAcceptance{Team: Team(existing.Team), Operation: existing.Operation, Replayed: true}, nil
	}
	if err := insertTeamRows(ctx, tx, team, request.CreatedByUserID); err != nil {
		return TeamOperationAcceptance{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM teams WHERE id=$1`, team.ID).Scan(&team.CreatedAt); err != nil {
		return TeamOperationAcceptance{}, err
	}
	team.CreatedAt = team.CreatedAt.UTC()
	operationID := uuid.NewString()
	revisionID, err := s.teamIntents.RecordWithOperation(ctx, tx, team.ID, false, operationID)
	if err != nil {
		return TeamOperationAcceptance{}, fmt.Errorf("record accepted team intent: %w", err)
	}
	accepted, err := s.teamIntents.RecordAcceptance(ctx, tx, teamintent.OperationRequest{
		ID: operationID, RevisionID: revisionID, ActorID: request.ActorID,
		Kind: apikit.OperationTeamCreate, IdempotencyKey: request.IdempotencyKey,
		RequestHash: requestHash, Team: apikit.Team(team),
	})
	if err != nil {
		return TeamOperationAcceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamOperationAcceptance{}, err
	}
	return TeamOperationAcceptance{Team: Team(accepted.Team), Operation: accepted.Operation}, nil
}

// GetTeamOperation reads the actor-scoped acceptance without deriving execution
// success from the resource's current health or current desired revision.
func (s *Store) GetTeamOperation(ctx context.Context, operationID, actorID string) (TeamOperationAcceptance, bool, error) {
	if s == nil || s.teamIntents == nil {
		return TeamOperationAcceptance{}, false, teamintent.ErrDisabled
	}
	accepted, found, err := s.teamIntents.GetOperation(ctx, operationID, actorID)
	return TeamOperationAcceptance{Team: Team(accepted.Team), Operation: accepted.Operation}, found, err
}
