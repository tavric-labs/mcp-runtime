package directoryapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-platform-api/internal/platformstore"
	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/passport"
)

type teamStore struct {
	Store
	create             platformstore.TeamCreateAcceptanceRequest
	delete             platformstore.TeamDeleteAcceptanceRequest
	actor, operationID string
	calls              int
	err                error
	found              bool
	accepted           platformstore.TeamOperationAcceptance
}

func (s *teamStore) AcceptTeamCreate(_ context.Context, req platformstore.TeamCreateAcceptanceRequest) (platformstore.TeamOperationAcceptance, error) {
	s.calls++
	s.create = req
	return s.accepted, s.err
}
func (s *teamStore) AcceptTeamDelete(_ context.Context, req platformstore.TeamDeleteAcceptanceRequest) (platformstore.TeamOperationAcceptance, error) {
	s.calls++
	s.delete = req
	return s.accepted, s.err
}
func (s *teamStore) GetTeamOperation(_ context.Context, id, actor string) (platformstore.TeamOperationAcceptance, bool, error) {
	s.calls++
	s.actor, s.operationID = actor, id
	return s.accepted, s.found, s.err
}

func directoryRequest(method, path, body string, p passport.Principal) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Idempotency-Key", "lost-response-key")
	return r.WithContext(passport.WithPrincipal(r.Context(), p))
}

func TestTeamCreateAcceptsWithoutProvisioningAndBindsAuthenticatedActor(t *testing.T) {
	s := &teamStore{accepted: platformstore.TeamOperationAcceptance{
		Team:      platformstore.Team{ID: "team-id", Slug: "team-a", Namespace: "mcp-team-team-a"},
		Operation: apikit.Operation{ID: "operation-id", State: apikit.OperationPending},
	}}
	r := directoryRequest(http.MethodPost, "/api/v1/teams", `{"slug":"team-a","name":"Team A"}`, passport.Principal{Role: passport.RoleAdmin, Subject: "owner-id"})
	r.Header.Set("x-api-key", "untrusted-ignored-header")
	w := httptest.NewRecorder()
	Handler{Store: s}.Teams(w, r)
	if w.Code != http.StatusAccepted || w.Header().Get("Location") != "/api/v1/operations/operation-id" {
		t.Fatalf("acceptance status=%d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if s.create.ActorID != "user:owner-id" || s.create.CreatedByUserID != "owner-id" || s.create.IdempotencyKey != "lost-response-key" {
		t.Fatalf("acceptance identity not bound to principal: %+v", s.create)
	}
	var body platformstore.TeamOperationAcceptance
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Team.ID != "team-id" || body.Operation.State != apikit.OperationPending {
		t.Fatalf("acceptance body=%s err=%v", w.Body.String(), err)
	}
}

func TestTeamMutationsRejectBeforeAcceptance(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, body, key string
		p                             passport.Principal
		want                          int
	}{
		{"unauthenticated", "POST", "/api/v1/teams", `{}`, "key", passport.Principal{}, 401},
		{"member create", "POST", "/api/v1/teams", `{}`, "key", passport.Principal{Role: passport.RoleUser, Subject: "user"}, 403},
		{"member delete", "DELETE", "/api/v1/teams/team-a", ``, "key", passport.Principal{Role: passport.RoleUser, Subject: "user"}, 403},
		{"missing replay key", "POST", "/api/v1/teams", `{"slug":"team-a"}`, "", passport.Principal{Role: passport.RoleAdmin, Subject: "user"}, 400},
		{"oversize replay key", "POST", "/api/v1/teams", `{"slug":"team-a"}`, strings.Repeat("x", 129), passport.Principal{Role: passport.RoleAdmin, Subject: "user"}, 400},
		{"forged actor", "POST", "/api/v1/teams", `{"slug":"team-a","actor_id":"other"}`, "key", passport.Principal{Role: passport.RoleAdmin, Subject: "user"}, 400},
		{"trailing body", "POST", "/api/v1/teams", `{"slug":"team-a"}{}`, "key", passport.Principal{Role: passport.RoleAdmin, Subject: "user"}, 400},
		{"no actor", "POST", "/api/v1/teams", `{"slug":"team-a"}`, "key", passport.Principal{Role: passport.RoleAdmin}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &teamStore{}
			r := directoryRequest(tt.method, tt.path, tt.body, tt.p)
			r.Header.Set("Idempotency-Key", tt.key)
			w := httptest.NewRecorder()
			h := Handler{Store: s}
			if tt.method == http.MethodDelete {
				h.Team(w, r)
			} else {
				h.Teams(w, r)
			}
			if w.Code != tt.want || s.calls != 0 {
				t.Fatalf("status=%d want=%d accepted=%d body=%s", w.Code, tt.want, s.calls, w.Body.String())
			}
		})
	}
}

func TestTeamAcceptanceFailureDoesNotExposeDatabaseDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{teamintent.ErrIdempotencyConflict, 409}, {teamintent.ErrDisabled, 503},
		{errors.New("private SQL diagnostic and credential"), 500},
	} {
		s := &teamStore{err: tt.err}
		w := httptest.NewRecorder()
		Handler{Store: s}.Teams(w, directoryRequest("POST", "/api/v1/teams", `{"slug":"team-a"}`, passport.Principal{Role: passport.RoleAdmin, Subject: "owner"}))
		if w.Code != tt.want || strings.Contains(w.Body.String(), "private SQL") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestTeamDeleteReturnsDurableCleanupAcceptance(t *testing.T) {
	s := &teamStore{accepted: platformstore.TeamOperationAcceptance{Operation: apikit.Operation{ID: "delete-operation", State: apikit.OperationPending}}}
	w := httptest.NewRecorder()
	Handler{Store: s}.Team(w, directoryRequest("DELETE", "/api/v1/teams/team-a", "", passport.Principal{Role: passport.RoleAdmin, Subject: "owner"}))
	if w.Code != 202 || s.delete.Slug != "team-a" || s.delete.ActorID != "user:owner" {
		t.Fatalf("status=%d request=%+v body=%s", w.Code, s.delete, w.Body.String())
	}
}

func TestOperationReadIsActorScoped(t *testing.T) {
	s := &teamStore{found: false}
	w := httptest.NewRecorder()
	Handler{Store: s}.Operation(w, directoryRequest("GET", "/api/v1/operations/other-operation", "", passport.Principal{Role: passport.RoleAdmin, Subject: "owner"}))
	if w.Code != 404 || s.actor != "user:owner" || s.operationID != "other-operation" {
		t.Fatalf("status=%d actor=%q operation=%q", w.Code, s.actor, s.operationID)
	}
}

func TestTeamStatusReportsCurrentFreshObservationWithoutInternalDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	intent := apikit.TeamNamespaceIntent{DesiredRevisionID: "current", ActiveOwnerTeamID: "internal-owner",
		Receipt:     &apikit.TeamNamespaceReceipt{AppliedRevisionID: "current", NamespaceUID: "namespace-uid", LastError: "private diagnostic"},
		Observation: &apikit.TeamNamespaceObservation{ObservedRevisionID: "current", NamespaceUID: "namespace-uid", Health: apikit.TeamIntentHealthReady, Reason: "NamespaceReady", LastVerifiedAt: now, Message: "private diagnostic"},
	}
	for _, tt := range []struct {
		name   string
		change func(*apikit.TeamNamespaceIntent)
		want   string
	}{
		{"fresh", func(*apikit.TeamNamespaceIntent) {}, "Ready"},
		{"stale", func(i *apikit.TeamNamespaceIntent) { i.Observation.LastVerifiedAt = now.Add(-2 * time.Minute) }, "Unknown"},
		{"wrong revision", func(i *apikit.TeamNamespaceIntent) { i.Observation.ObservedRevisionID = "old" }, "Progressing"},
		{"replacement UID", func(i *apikit.TeamNamespaceIntent) { i.Observation.NamespaceUID = "replacement" }, "Progressing"},
		{"unapplied", func(i *apikit.TeamNamespaceIntent) { i.Receipt = nil }, "Pending"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			copy := intent
			obs := *intent.Observation
			copy.Observation = &obs
			tt.change(&copy)
			status := teamStatus(copy, now)
			if status.Health != tt.want {
				t.Fatalf("health=%s want=%s", status.Health, tt.want)
			}
			body, _ := json.Marshal(status)
			if strings.Contains(string(body), "private") || strings.Contains(string(body), "internal-owner") {
				t.Fatalf("internal state exposed: %s", body)
			}
		})
	}
}
