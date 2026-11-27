package platformstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"mcp-platform-api/internal/teamintent"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
)

func TestPostgresTeamOperationAcceptanceReplay(t *testing.T) {
	s := openTeamIntentTestStore(t, "true")
	owner, err := s.CreatePasswordUser(t.Context(), "operation-owner@fixture.test", "fixture-password-only", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	request := TeamCreateAcceptanceRequest{
		Slug: "operation-team", Name: "Operation team", CreatedByUserID: owner.ID,
		ActorID: "user:" + owner.ID, IdempotencyKey: "lost-response-fixture",
	}
	first, err := s.AcceptTeamCreate(httpx.ContextWithRequestID(t.Context(), "accepted-request"), request)
	if err != nil {
		t.Fatalf("accept team create: %v", err)
	}
	if first.Operation.ID == "" || first.Operation.DesiredRevisionID == "" || first.Operation.ID == first.Operation.DesiredRevisionID {
		t.Fatalf("operation and revision identities must be distinct: %+v", first.Operation)
	}
	if first.Operation.State != apikit.OperationPending || first.Operation.RequestID != "accepted-request" || first.Replayed {
		t.Fatalf("unexpected acceptance: %+v", first)
	}
	second, err := s.AcceptTeamCreate(httpx.ContextWithRequestID(t.Context(), "retry-request"), request)
	if err != nil {
		t.Fatalf("replay lost response: %v", err)
	}
	if !second.Replayed || !reflect.DeepEqual(first.Team, second.Team) || !reflect.DeepEqual(first.Operation, second.Operation) {
		t.Fatalf("retry changed accepted resource or operation: first=%+v second=%+v", first, second)
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM teams WHERE slug='operation-team'`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 1)
	got, found, err := s.GetTeamOperation(t.Context(), first.Operation.ID, request.ActorID)
	if err != nil || !found || got.Operation.ID != first.Operation.ID {
		t.Fatalf("read operation: %+v found=%v err=%v", got, found, err)
	}
	if _, found, err = s.GetTeamOperation(t.Context(), first.Operation.ID, "user:another-owner"); err != nil || found {
		t.Fatalf("operation crossed actor scope: found=%v err=%v", found, err)
	}
}

func operationFixture(t *testing.T) (*Store, TeamCreateAcceptanceRequest) {
	t.Helper()
	s := openTeamIntentTestStore(t, "true")
	owner, err := s.CreatePasswordUser(t.Context(), "operation-owner@fixture.test", "fixture-password-only", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return s, TeamCreateAcceptanceRequest{Slug: "operation-team", Name: "Operation team", CreatedByUserID: owner.ID,
		ActorID: "user:" + owner.ID, IdempotencyKey: "operation-fixture-key"}
}

func TestPostgresTeamOperationReplayAfterStoreRestart(t *testing.T) {
	dsn := teamIntentTestDatabase(t)
	t.Setenv("PLATFORM_TEAM_INTENT_JOURNAL", "true")
	firstStore, err := Open(t.Context(), dsn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	owner, err := firstStore.CreatePasswordUser(t.Context(), "restart-owner@fixture.test", "fixture-password-only", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	request := TeamCreateAcceptanceRequest{Slug: "restart-team", Name: "Restart team", CreatedByUserID: owner.ID,
		ActorID: "user:" + owner.ID, IdempotencyKey: "response-lost-before-restart"}
	accepted, err := firstStore.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(t.Context(), dsn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	replayed, err := secondStore.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(accepted.Team, replayed.Team) || !reflect.DeepEqual(accepted.Operation, replayed.Operation) {
		t.Fatalf("restart lost acceptance: accepted=%+v replayed=%+v", accepted, replayed)
	}
	assertTeamIntentCount(t, secondStore, `SELECT count(*) FROM teams`, 1)
	assertTeamIntentCount(t, secondStore, `SELECT count(*) FROM team_intent.operation`, 1)
	assertTeamIntentCount(t, secondStore, `SELECT count(*) FROM team_intent_queue.river_job`, 1)
}

func TestPostgresTeamOperationDeleteCommitsAndReplaysAfterReplacement(t *testing.T) {
	s, create := operationFixture(t)
	accepted, err := s.AcceptTeamCreate(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	request := TeamDeleteAcceptanceRequest{Slug: create.Slug, ActorID: create.ActorID, IdempotencyKey: "delete-response-lost"}
	deleted, err := s.AcceptTeamDelete(t.Context(), request)
	if err != nil {
		t.Fatalf("accept deletion: %v", err)
	}
	if deleted.Team.ID != accepted.Team.ID || deleted.Operation.Kind != "team.delete" || deleted.Operation.DesiredRevisionID == accepted.Operation.DesiredRevisionID {
		t.Fatalf("deletion lost distinct acceptance: %+v", deleted)
	}
	assertTeamIntentDomainState(t, s, accepted.Team, true)
	prior, _, err := s.GetTeamOperation(t.Context(), accepted.Operation.ID, create.ActorID)
	if err != nil || prior.Operation.State != apikit.OperationSuperseded {
		t.Fatalf("pending create was not superseded: %+v err=%v", prior, err)
	}
	create.IdempotencyKey = "replacement-create"
	replacement, err := s.AcceptTeamCreate(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.AcceptTeamDelete(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Team.ID != deleted.Team.ID || replayed.Operation.ID != deleted.Operation.ID {
		t.Fatalf("delete replay targeted replacement: %+v err=%v", replayed, err)
	}
	assertTeamIntentDomainState(t, s, replacement.Team, false)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 3)
}

func TestPostgresTeamOperationConcurrentReplayCreatesOneAcceptance(t *testing.T) {
	s, request := operationFixture(t)
	type result struct {
		accepted TeamOperationAcceptance
		err      error
	}
	const callers = 8
	start := make(chan struct{})
	results := make(chan result, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			<-start
			accepted, err := s.AcceptTeamCreate(t.Context(), request)
			results <- result{accepted, err}
		})
	}
	close(start)
	group.Wait()
	close(results)
	var id, revision, teamID string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == "" {
			id = result.accepted.Operation.ID
			revision = result.accepted.Operation.DesiredRevisionID
			teamID = result.accepted.Team.ID
		}
		if result.accepted.Operation.ID != id || result.accepted.Operation.DesiredRevisionID != revision || result.accepted.Team.ID != teamID {
			t.Fatalf("concurrent retry created another acceptance: %+v", result.accepted)
		}
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM teams WHERE slug='operation-team'`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent.operation`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 1)
	var payload struct {
		OperationID string `json:"operationId"`
		RevisionID  string `json:"revisionId"`
	}
	var raw []byte
	if err := s.db.QueryRowContext(t.Context(), `SELECT args FROM team_intent_queue.river_job`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OperationID != id || payload.RevisionID != revision {
		t.Fatalf("job lost acceptance identity: %+v", payload)
	}
}

func TestPostgresTeamOperationReplayConflictAndActorIsolation(t *testing.T) {
	s, request := operationFixture(t)
	first, err := s.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Slug = "different-team"
	if _, err = s.AcceptTeamCreate(t.Context(), changed); !errors.Is(err, teamintent.ErrIdempotencyConflict) {
		t.Fatalf("changed request with same key: %v", err)
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM teams`, 1)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 1)
	changed.ActorID = "user:another-actor"
	second, err := s.AcceptTeamCreate(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Operation.ID == first.Operation.ID || second.Team.ID == first.Team.ID {
		t.Fatal("replay key crossed actor scope")
	}
}

func TestPostgresTeamOperationQueueFailureRollsBackEntireAcceptance(t *testing.T) {
	s, request := operationFixture(t)
	if _, err := s.db.ExecContext(t.Context(), `
CREATE FUNCTION team_intent_queue.reject_operation_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture queue insert failure'; END $$;
CREATE TRIGGER reject_operation_job BEFORE INSERT ON team_intent_queue.river_job
FOR EACH ROW EXECUTE FUNCTION team_intent_queue.reject_operation_job();`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptTeamCreate(t.Context(), request); err == nil {
		t.Fatal("queue failure accepted team")
	}
	for _, table := range []string{"teams", "team_memberships", "team_intent.team_namespace_intent", "team_intent.operation", "team_intent_queue.river_job"} {
		assertTeamIntentCount(t, s, `SELECT count(*) FROM `+table, 0)
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM namespaces WHERE team_id IS NOT NULL`, 0)
	if _, err := s.db.ExecContext(t.Context(), `DROP TRIGGER reject_operation_job ON team_intent_queue.river_job`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptTeamCreate(t.Context(), request); err != nil {
		t.Fatalf("rolled-back key cannot be retried: %v", err)
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent.operation`, 1)
}

func TestPostgresTeamOperationExecutionIsSeparateFromHealthAndCurrentIntent(t *testing.T) {
	s, request := operationFixture(t)
	accepted, err := s.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimTeamIntentFence(t.Context(), accepted.Team.ID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	op, found, err := s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || !found || op.Operation.State != apikit.OperationRunning {
		t.Fatalf("claim status: %+v found=%v err=%v", op, found, err)
	}
	_, err = s.RecordTeamIntentReceipt(t.Context(), accepted.Team.ID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: accepted.Operation.DesiredRevisionID, FenceEpoch: claim.FenceEpoch,
		Outcome: apikit.TeamIntentOutcomeApplied, NamespaceUID: "namespace-uid", WorkerID: "worker-1", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || op.Operation.State != apikit.OperationSucceeded {
		t.Fatalf("applied status: %+v err=%v", op, err)
	}
	intent, _, err := s.GetTeamIntent(t.Context(), accepted.Team.ID)
	if err != nil || intent.Observation != nil {
		t.Fatalf("execution fabricated resource observation: %+v err=%v", intent, err)
	}
	if err = s.DeleteTeamBySlug(t.Context(), request.Slug); err != nil {
		t.Fatal(err)
	}
	op, _, err = s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || op.Operation.State != apikit.OperationSucceeded || !reflect.DeepEqual(op.Team, accepted.Team) {
		t.Fatalf("new desired state rewrote earlier success or snapshot: %+v err=%v", op, err)
	}
}

func TestPostgresTeamOperationSupersededAndStaleReceipts(t *testing.T) {
	s, request := operationFixture(t)
	accepted, err := s.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimTeamIntentFence(t.Context(), accepted.Team.ID, "worker-old")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTeamBySlug(t.Context(), request.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordTeamIntentReceipt(t.Context(), accepted.Team.ID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: accepted.Operation.DesiredRevisionID, FenceEpoch: claim.FenceEpoch,
		Outcome: apikit.TeamIntentOutcomeApplied, NamespaceUID: "old-uid", WorkerID: "worker-old", Attempt: 1}); !errors.Is(err, teamintent.ErrStaleReceipt) {
		t.Fatalf("stale receipt was accepted: %v", err)
	}
	op, _, err := s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || op.Operation.State != apikit.OperationSuperseded {
		t.Fatalf("stale receipt changed superseded operation: %+v err=%v", op, err)
	}
}

func TestTeamOperationAcceptanceRequiresEnabledJournal(t *testing.T) {
	var store *Store
	if _, err := store.AcceptTeamCreate(t.Context(), TeamCreateAcceptanceRequest{}); !errors.Is(err, teamintent.ErrDisabled) {
		t.Fatalf("disabled acceptance error=%v", err)
	}
}

func TestPostgresTeamOperationPersistenceFailureRollsBackJob(t *testing.T) {
	s, request := operationFixture(t)
	if _, err := s.db.ExecContext(t.Context(), `
CREATE FUNCTION team_intent.reject_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture operation insert failure'; END $$;
CREATE TRIGGER reject_operation BEFORE INSERT ON team_intent.operation
FOR EACH ROW EXECUTE FUNCTION team_intent.reject_operation();`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptTeamCreate(t.Context(), request); err == nil {
		t.Fatal("operation failure accepted team or job")
	}
	for _, table := range []string{"teams", "team_intent.operation", "team_intent.team_namespace_intent", "team_intent_queue.river_job"} {
		assertTeamIntentCount(t, s, `SELECT count(*) FROM `+table, 0)
	}
}

func TestPostgresTeamOperationNormalizedReplayAndSafeFailureMetadata(t *testing.T) {
	s, request := operationFixture(t)
	accepted, err := s.AcceptTeamCreate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	normalized := request
	normalized.Slug = " Operation-Team "
	normalized.Name = " Operation team "
	normalized.CreatedByUserID = " " + request.CreatedByUserID + " "
	if replay, err := s.AcceptTeamCreate(t.Context(), normalized); err != nil || replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("normalized replay changed acceptance: %+v err=%v", replay, err)
	}
	claim, err := s.ClaimTeamIntentFence(t.Context(), accepted.Team.ID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordTeamIntentReceipt(t.Context(), accepted.Team.ID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: accepted.Operation.DesiredRevisionID, FenceEpoch: claim.FenceEpoch,
		Outcome: apikit.TeamIntentOutcomeFailed, WorkerID: "worker-1", Attempt: 1,
		LastErrorClass: apikit.TeamIntentErrorTransient, LastError: "fixture private backend diagnostic",
	})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || op.Operation.State != apikit.OperationRunning || op.Operation.ErrorClass != apikit.TeamIntentErrorTransient {
		t.Fatalf("transient failure became terminal: %+v err=%v", op, err)
	}
	_, err = s.RecordTeamIntentReceipt(t.Context(), accepted.Team.ID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: accepted.Operation.DesiredRevisionID, FenceEpoch: claim.FenceEpoch,
		Outcome: apikit.TeamIntentOutcomeTerminal, WorkerID: "worker-1", Attempt: 2,
		LastErrorClass: apikit.TeamIntentErrorInvalidConfig, LastError: "fixture private backend diagnostic",
	})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = s.GetTeamOperation(t.Context(), accepted.Operation.ID, request.ActorID)
	if err != nil || op.Operation.State != apikit.OperationFailed || op.Operation.ErrorClass != apikit.TeamIntentErrorInvalidConfig {
		t.Fatalf("terminal failure not surfaced: %+v err=%v", op, err)
	}
	wire, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), request.IdempotencyKey) || strings.Contains(string(wire), "private backend diagnostic") {
		t.Fatalf("operation exposed a replay key or raw backend diagnostic: %s", wire)
	}
}

func TestPostgresTeamOperationRejectsMissingReplayScope(t *testing.T) {
	s, request := operationFixture(t)
	for _, change := range []func(*TeamCreateAcceptanceRequest){
		func(r *TeamCreateAcceptanceRequest) { r.ActorID = "" },
		func(r *TeamCreateAcceptanceRequest) { r.IdempotencyKey = "" },
		func(r *TeamCreateAcceptanceRequest) { r.IdempotencyKey = "contains whitespace" },
		func(r *TeamCreateAcceptanceRequest) { r.IdempotencyKey = strings.Repeat("x", 129) },
	} {
		invalid := request
		change(&invalid)
		if _, err := s.AcceptTeamCreate(t.Context(), invalid); !errors.Is(err, teamintent.ErrInvalidInput) {
			t.Fatalf("invalid replay scope error=%v", err)
		}
	}
	assertTeamIntentCount(t, s, `SELECT count(*) FROM teams`, 0)
	assertTeamIntentCount(t, s, `SELECT count(*) FROM team_intent_queue.river_job`, 0)
}
