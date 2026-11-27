// Package directoryapi serves platform-owned directory resources and durable
// infrastructure acceptances without calling the runtime during a request.
package directoryapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lib/pq"

	"mcp-platform-api/internal/platformstore"
	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
	"mcp-runtime/pkg/passport"
)

type Store interface {
	AcceptTeamCreate(context.Context, platformstore.TeamCreateAcceptanceRequest) (platformstore.TeamOperationAcceptance, error)
	AcceptTeamDelete(context.Context, platformstore.TeamDeleteAcceptanceRequest) (platformstore.TeamOperationAcceptance, error)
	GetTeamOperation(context.Context, string, string) (platformstore.TeamOperationAcceptance, bool, error)
	ListTeams(context.Context) ([]platformstore.Team, error)
	GetTeamBySlug(context.Context, string) (platformstore.Team, bool, error)
	GetTeamIntent(context.Context, string) (apikit.TeamNamespaceIntent, bool, error)
}

type Handler struct{ Store Store }

func (h Handler) Teams(w http.ResponseWriter, r *http.Request) {
	p, ok := authenticated(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w, "GET, POST")
		return
	}
	if r.Method == http.MethodPost && p.Role != passport.RoleAdmin {
		forbidden(w)
		return
	}
	if h.Store == nil {
		unavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		if p.Role != passport.RoleAdmin {
			teams := make([]map[string]string, 0, len(p.Teams))
			for _, team := range p.Teams {
				teams = append(teams, map[string]string{"id": team.ID, "slug": team.Slug, "name": team.Name, "namespace": team.Namespace, "role": team.Role})
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"teams": teams})
			return
		}
		teams, err := h.Store.ListTeams(ctx)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"teams": teams})
		return
	}
	actor, key, ok := mutationIdentity(w, r, p)
	if !ok {
		return
	}
	var req struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := platformstore.ValidateTeamSlug(platformstore.NormalizeTeamSlug(req.Slug)); err != nil {
		apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, err.Error())
		return
	}
	accepted, err := h.Store.AcceptTeamCreate(ctx, platformstore.TeamCreateAcceptanceRequest{
		Slug: req.Slug, Name: req.Name, CreatedByUserID: p.UserID(), ActorID: actor, IdempotencyKey: key,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeAcceptance(w, accepted)
}

func (h Handler) Team(w http.ResponseWriter, r *http.Request) {
	p, ok := authenticated(w, r)
	if !ok {
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")
	if strings.Contains(slug, "/") || slug == "" {
		notFound(w)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		methodNotAllowed(w, "GET, DELETE")
		return
	}
	if r.Method == http.MethodDelete && p.Role != passport.RoleAdmin {
		forbidden(w)
		return
	}
	if r.Method == http.MethodGet && p.Role != passport.RoleAdmin && p.TeamRole(slug) == "" {
		forbidden(w)
		return
	}
	if h.Store == nil {
		unavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.Method == http.MethodDelete {
		actor, key, ok := mutationIdentity(w, r, p)
		if !ok {
			return
		}
		if err := platformstore.ValidateTeamSlug(platformstore.NormalizeTeamSlug(slug)); err != nil {
			apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, err.Error())
			return
		}
		accepted, err := h.Store.AcceptTeamDelete(ctx, platformstore.TeamDeleteAcceptanceRequest{Slug: slug, ActorID: actor, IdempotencyKey: key})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeAcceptance(w, accepted)
		return
	}
	team, found, err := h.Store.GetTeamBySlug(ctx, slug)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		notFound(w)
		return
	}
	intent, found, err := h.Store.GetTeamIntent(ctx, team.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var status *apikit.ResourceStatus
	if found {
		current := teamStatus(intent, time.Now().UTC())
		status = &current
	}
	httpx.WriteJSON(w, 200, map[string]any{"team": team, "status": status})
}

// Three missed default observer intervals make health unknown. This is a
// display/readiness freshness budget, never an authorization grace period.
const observationFreshness = 90 * time.Second

func teamStatus(intent apikit.TeamNamespaceIntent, now time.Time) apikit.ResourceStatus {
	status := apikit.ResourceStatus{DesiredRevisionID: intent.DesiredRevisionID, Health: apikit.TeamIntentHealthPending, Reason: "AwaitingApplication"}
	if intent.Receipt == nil {
		return status
	}
	status.AppliedRevisionID = intent.Receipt.AppliedRevisionID
	if status.AppliedRevisionID != status.DesiredRevisionID {
		return status
	}
	status.Health, status.Reason = apikit.TeamIntentHealthProgressing, "AwaitingObservation"
	obs := intent.Observation
	if obs == nil {
		return status
	}
	verified := obs.LastVerifiedAt.UTC()
	status.LastVerifiedAt = &verified
	status.ObservedRevisionID = obs.ObservedRevisionID
	if obs.ObservedRevisionID != status.DesiredRevisionID || obs.NamespaceUID != intent.Receipt.NamespaceUID {
		return status
	}
	if verified.IsZero() || now.Sub(verified) > observationFreshness || verified.After(now.Add(5*time.Second)) {
		status.Health, status.Reason = apikit.TeamIntentHealthUnknown, "ObservationStale"
		return status
	}
	status.Health, status.Reason = obs.Health, obs.Reason
	return status
}

func (h Handler) Operation(w http.ResponseWriter, r *http.Request) {
	p, ok := authenticated(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/operations/")
	if id == "" || len(id) > 128 || strings.Contains(id, "/") {
		notFound(w)
		return
	}
	actor, err := actorIdentity(r, p)
	if err != nil {
		forbidden(w)
		return
	}
	if h.Store == nil {
		unavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	accepted, found, err := h.Store.GetTeamOperation(ctx, id, actor)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		notFound(w)
		return
	}
	httpx.WriteJSON(w, 200, accepted)
}

func authenticated(w http.ResponseWriter, r *http.Request) (passport.Principal, bool) {
	p, ok := passport.FromContext(r.Context())
	if !ok || p.Role == "" {
		apikit.WriteEnvelope(w, 401, apikit.CodeUnauthorized, "authentication required")
		return p, false
	}
	return p, true
}

// The identity comes exclusively from authenticated context. Static service
// keys have no subject; only that verified auth mode may hash its request key.
// A human's optional x-api-key header cannot change the human's replay scope.
func actorIdentity(r *http.Request, p passport.Principal) (string, error) {
	if p.IsService {
		if p.APIKeyID != "" {
			return "service:" + p.APIKeyID, nil
		}
		if p.AuthType == "service_api_key" {
			if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
				digest := sha256.Sum256([]byte(key))
				return "service:" + hex.EncodeToString(digest[:]), nil
			}
		}
		return "", fmt.Errorf("stable service identity required")
	}
	if id := p.UserID(); id != "" {
		return "user:" + id, nil
	}
	return "", fmt.Errorf("authenticated actor required")
}

func mutationIdentity(w http.ResponseWriter, r *http.Request, p passport.Principal) (string, string, bool) {
	actor, err := actorIdentity(r, p)
	if err != nil {
		forbidden(w)
		return "", "", false
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 128 {
		apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, "Idempotency-Key must contain 1 to 128 visible ASCII characters")
		return "", "", false
	}
	for _, ch := range keys[0] {
		if ch < 33 || ch > 126 {
			apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, "invalid Idempotency-Key")
			return "", "", false
		}
	}
	return actor, keys[0], true
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, apikit.ApplyMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		apikit.WriteBodyDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, "expected one JSON object")
		return false
	}
	return true
}

func writeAcceptance(w http.ResponseWriter, accepted platformstore.TeamOperationAcceptance) {
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	w.Header().Set("Retry-After", "1")
	httpx.WriteJSON(w, http.StatusAccepted, accepted)
}

func writeStoreError(w http.ResponseWriter, err error) {
	var pgErr *pq.Error
	switch {
	case errors.Is(err, teamintent.ErrDisabled):
		unavailable(w)
	case errors.Is(err, teamintent.ErrIdempotencyConflict):
		apikit.WriteEnvelope(w, 409, apikit.CodeConflict, "replay key already accepted different input")
	case errors.Is(err, teamintent.ErrInvalidInput):
		apikit.WriteEnvelope(w, 400, apikit.CodeInvalidRequestBody, "invalid acceptance input")
	case errors.Is(err, sql.ErrNoRows):
		notFound(w)
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		apikit.WriteEnvelope(w, 409, apikit.CodeConflict, "team already exists")
	default:
		apikit.WriteEnvelope(w, 500, apikit.CodeInternalError, "directory operation failed")
	}
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	apikit.WriteEnvelope(w, 405, apikit.CodeMethodNotAllowed, "method not allowed")
}
func unavailable(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, 503, apikit.CodeServiceUnavailable, "durable directory unavailable")
}
func forbidden(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, 403, apikit.CodeForbidden, "insufficient permissions")
}
func notFound(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, 404, apikit.CodeNotFound, "resource not found")
}
