package accessintent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

type fakeJobInserter struct {
	inserted []river.JobArgs
	queues   []string
	err      error
}

func (f *fakeJobInserter) InsertTx(_ context.Context, _ *sql.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.inserted = append(f.inserted, args)
	if opts != nil {
		f.queues = append(f.queues, opts.Queue)
	}
	return &rivertype.JobInsertResult{}, nil
}

func TestActivateEnrollmentRejectsRevokedSessionAndInactiveAgent(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour).Format(time.RFC3339)

	t.Run("revoked session is never un-revoked and marks enrollment revoked", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		w := &Writer{db: db, queue: &fakeJobInserter{}}

		sess := sessionstate.Session{
			ID: "mcp-team-alpha/sess-1", UID: "sess-uid-1", Namespace: "mcp-team-alpha",
			ServerNamespace: "mcp-team-alpha", ServerName: "docs", ServerUID: "srv-uid-1",
			Binding: policy.Binding{
				Name: "sess-1", Namespace: "mcp-team-alpha", AgentID: "agt-1", TeamID: "team-1",
				Revoked: true, ExpiresAt: future,
			},
		}
		sessJSON, _ := json.Marshal(sess)

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(int64(7)))
		mock.ExpectQuery(`FROM access_intent\.enrollment_intent i`).
			WithArgs("op-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"session_uid", "server_uid", "agent_id", "namespace", "session_name",
				"desired_revision_id", "state", "certificate_request_name", "fence_epoch",
			}).AddRow("sess-uid-1", "srv-uid-1", "agt-1", "mcp-team-alpha", "sess-1", "rev-1", apikit.EnrollmentStateIssued, "enroll-abc", int64(2)))
		mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM session_retired_servers`).
			WithArgs("srv-uid-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectQuery(`SELECT data FROM sessions WHERE namespace = \$1 AND name = \$2`).
			WithArgs("mcp-team-alpha", "sess-1").
			WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(sessJSON))
		mock.ExpectExec(`UPDATE access_intent\.enrollment_intent`).
			WithArgs("op-1", apikit.EnrollmentStateRevoked).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE access_intent\.enrollment_receipt`).
			WithArgs("op-1", "enroll-abc", "cr-uid-1", apikit.AccessIntentOutcomeTerminal, "worker-1", apikit.AccessIntentErrorAuthorityRevoked, "session is revoked", int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		_, err = w.ActivateEnrollment(t.Context(), "op-1", apikit.EnrollmentIntentActivateRequest{
			DesiredRevisionID:     "rev-1",
			FenceEpoch:            2,
			WorkerID:              "worker-1",
			CertificateRequestUID: "cr-uid-1",
		})
		if !errors.Is(err, ErrAuthorityRevoked) {
			t.Fatalf("err = %v, want ErrAuthorityRevoked", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("inactive agent rejects activation and preserves session->agent lock order", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		w := &Writer{db: db, queue: &fakeJobInserter{}}

		sess := sessionstate.Session{
			ID: "mcp-team-alpha/sess-1", UID: "sess-uid-1", Namespace: "mcp-team-alpha",
			ServerNamespace: "mcp-team-alpha", ServerName: "docs", ServerUID: "srv-uid-1",
			Binding: policy.Binding{
				Name: "sess-1", Namespace: "mcp-team-alpha", AgentID: "agt-1", TeamID: "team-1",
				Revoked: false, ExpiresAt: future,
			},
		}
		sessJSON, _ := json.Marshal(sess)

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(int64(7)))
		mock.ExpectQuery(`FROM access_intent\.enrollment_intent i`).
			WithArgs("op-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"session_uid", "server_uid", "agent_id", "namespace", "session_name",
				"desired_revision_id", "state", "certificate_request_name", "fence_epoch",
			}).AddRow("sess-uid-1", "srv-uid-1", "agt-1", "mcp-team-alpha", "sess-1", "rev-1", apikit.EnrollmentStateIssued, "enroll-abc", int64(2)))
		mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM session_retired_servers`).
			WithArgs("srv-uid-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectQuery(`SELECT data FROM sessions WHERE namespace = \$1 AND name = \$2`).
			WithArgs("mcp-team-alpha", "sess-1").
			WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(sessJSON))
		mock.ExpectQuery(`SELECT status, team_id::text FROM agents WHERE id = \$1 FOR UPDATE`).
			WithArgs("agt-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "team_id"}).AddRow("inactive", "team-1"))
		mock.ExpectExec(`UPDATE access_intent\.enrollment_intent`).
			WithArgs("op-1", apikit.EnrollmentStateRevoked).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE access_intent\.enrollment_receipt`).
			WithArgs("op-1", "enroll-abc", "cr-uid-1", apikit.AccessIntentOutcomeTerminal, "worker-1", apikit.AccessIntentErrorAuthorityRevoked, "bound agent is inactive or team mismatched", int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		_, err = w.ActivateEnrollment(t.Context(), "op-1", apikit.EnrollmentIntentActivateRequest{
			DesiredRevisionID:     "rev-1",
			FenceEpoch:            2,
			WorkerID:              "worker-1",
			CertificateRequestUID: "cr-uid-1",
		})
		if !errors.Is(err, ErrAuthorityRevoked) {
			t.Fatalf("err = %v, want ErrAuthorityRevoked", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("stale fence epoch is rejected with ErrStaleReceipt", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		w := &Writer{db: db, queue: &fakeJobInserter{}}

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(int64(7)))
		mock.ExpectQuery(`FROM access_intent\.enrollment_intent i`).
			WithArgs("op-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"session_uid", "server_uid", "agent_id", "namespace", "session_name",
				"desired_revision_id", "state", "certificate_request_name", "fence_epoch",
			}).AddRow("sess-uid-1", "srv-uid-1", "agt-1", "mcp-team-alpha", "sess-1", "rev-1", apikit.EnrollmentStateIssued, "enroll-abc", int64(3)))
		mock.ExpectRollback()

		_, err = w.ActivateEnrollment(t.Context(), "op-1", apikit.EnrollmentIntentActivateRequest{
			DesiredRevisionID: "rev-1",
			FenceEpoch:        2,
			WorkerID:          "worker-1",
		})
		if !errors.Is(err, ErrStaleReceipt) {
			t.Fatalf("err = %v, want ErrStaleReceipt", err)
		}
	})
}

func TestRecordGrantObservationRejectsUnverifiedClaimsAndStaleEpochs(t *testing.T) {
	t.Run("stale observer epoch returns ErrStaleObservation", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		w := &Writer{db: db, queue: &fakeJobInserter{}}

		mock.ExpectBegin()
		mock.ExpectQuery(`FROM access_intent\.grant_intent i`).
			WithArgs("grant-1").
			WillReturnRows(sqlmock.NewRows([]string{"desired_revision_id", "state", "applied_revision_id", "target_uid"}).
				AddRow("rev-1", apikit.GrantIntentStateActive, "rev-1", "uid-1"))
		mock.ExpectQuery(`SELECT observer_epoch FROM access_intent\.observer_lease`).
			WithArgs("default").
			WillReturnRows(sqlmock.NewRows([]string{"observer_epoch"}).AddRow(int64(5)))
		mock.ExpectRollback()

		_, err = w.RecordGrantObservation(t.Context(), "grant-1", apikit.GrantIntentObservationRequest{
			ObserverID:           "obs-old",
			ObserverEpoch:        4,
			ObservedRevisionID:   "rev-1",
			TargetUID:            "uid-1",
			Health:               apikit.AccessIntentHealthReady,
			PropagationStatus:    apikit.GrantPropagationVerified,
			ServingReplicas:      2,
			AcknowledgedReplicas: 2,
		})
		if !errors.Is(err, ErrStaleObservation) {
			t.Fatalf("err = %v, want ErrStaleObservation", err)
		}
	})

	t.Run("verified propagation rejected when serving replicas only partially acked", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		w := &Writer{db: db, queue: &fakeJobInserter{}}

		mock.ExpectBegin()
		mock.ExpectQuery(`FROM access_intent\.grant_intent i`).
			WithArgs("grant-1").
			WillReturnRows(sqlmock.NewRows([]string{"desired_revision_id", "state", "applied_revision_id", "target_uid"}).
				AddRow("rev-1", apikit.GrantIntentStateActive, "rev-1", "uid-1"))
		mock.ExpectQuery(`SELECT observer_epoch FROM access_intent\.observer_lease`).
			WithArgs("default").
			WillReturnRows(sqlmock.NewRows([]string{"observer_epoch"}).AddRow(int64(2)))
		mock.ExpectQuery(`FROM access_intent\.grant_observation`).
			WithArgs("grant-1").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		_, err = w.RecordGrantObservation(t.Context(), "grant-1", apikit.GrantIntentObservationRequest{
			ObserverID:           "obs-1",
			ObserverEpoch:        2,
			ObservedRevisionID:   "rev-1",
			TargetUID:            "uid-1",
			Health:               apikit.AccessIntentHealthReady,
			PropagationStatus:    apikit.GrantPropagationVerified,
			ServingReplicas:      3,
			AcknowledgedReplicas: 2,
		})
		if !errors.Is(err, ErrStaleObservation) {
			t.Fatalf("err = %v, want ErrStaleObservation", err)
		}
	})
}

func TestRecordAgentDeactivationTxEnqueuesOnAccessControlQueue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queue := &fakeJobInserter{}
	w := &Writer{db: db, queue: queue}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO access_intent\.agent_cleanup_intent`).
		WithArgs("agt-1", "team-1", sqlmock.AnyArg(), sqlmock.AnyArg(), int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RecordAgentDeactivationTx(t.Context(), tx, "agt-1", "team-1", time.Now().UTC(), 3); err != nil {
		t.Fatalf("RecordAgentDeactivationTx error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(queue.inserted) != 1 || queue.queues[0] != QueueAccessControl {
		t.Fatalf("enqueued = %+v queues = %+v", queue.inserted, queue.queues)
	}
	job, ok := queue.inserted[0].(ReconcileAgentCleanupJob)
	if !ok || job.AgentID != "agt-1" || job.RevisionID == "" {
		t.Fatalf("unexpected job payload: %#v", queue.inserted[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAccessIntentPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("MCP_PLATFORM_ACCESS_INTENT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MCP_PLATFORM_ACCESS_INTENT_TEST_POSTGRES_DSN to run PostgreSQL access intent integration tests")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	ctx := t.Context()
	w, err := Open(ctx, db, dsn)
	if err != nil {
		t.Fatalf("Open access intent writer: %v", err)
	}

	grant, err := w.UpsertGrantIntent(ctx, apikit.GrantIntentUpsertRequest{
		Namespace:  "mcp-team-int",
		Name:       "allow-tools",
		TeamID:     "team-int",
		ServerName: "docs",
		ServerUID:  "srv-uid-int",
		Subject:    apikit.GrantSubjectSummary{HumanID: "user-1", TeamID: "team-int"},
		SpecJSON:   json.RawMessage(`{"mode":"enforce"}`),
	})
	if err != nil {
		t.Fatalf("UpsertGrantIntent: %v", err)
	}
	if grant.State != apikit.GrantIntentStateActive || grant.DesiredRevisionID == "" {
		t.Fatalf("unexpected grant intent: %+v", grant)
	}

	fence1, err := w.ClaimGrantFence(ctx, grant.GrantID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimGrantFence 1: %v", err)
	}
	fence2, err := w.ClaimGrantFence(ctx, grant.GrantID, "worker-b")
	if err != nil {
		t.Fatalf("ClaimGrantFence 2: %v", err)
	}
	if fence2.FenceEpoch <= fence1.FenceEpoch {
		t.Fatalf("expected monotonic fence epoch: %d vs %d", fence2.FenceEpoch, fence1.FenceEpoch)
	}

	// Stale fence rejected.
	if _, err := w.RecordGrantReceipt(ctx, grant.GrantID, apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: grant.DesiredRevisionID,
		FenceEpoch:        fence1.FenceEpoch,
		Outcome:           apikit.AccessIntentOutcomeApplied,
		TargetUID:         "k8s-uid-1",
		TargetGeneration:  1,
		SpecDigest:        "sha256:abc",
		WorkerID:          "worker-a",
	}); !errors.Is(err, ErrStaleReceipt) {
		t.Fatalf("stale receipt err = %v, want ErrStaleReceipt", err)
	}

	// Revoke commits synchronously BEFORE projection/propagation.
	revoked, err := w.RevokeGrantIntent(ctx, grant.GrantID, true)
	if err != nil {
		t.Fatalf("RevokeGrantIntent: %v", err)
	}
	if !revoked.GatewayPropagation.RevocationCommitted || revoked.GatewayPropagation.ProjectionApplied || revoked.GatewayPropagation.PropagationVerified {
		t.Fatalf("revocation milestones must remain distinct: %+v", revoked.GatewayPropagation)
	}

	// Idempotent enrollment creation on (session_uid, server_uid, csr_sha256).
	csrDigest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	en1, created1, err := w.CreateEnrollmentIntent(ctx, apikit.EnrollmentIntentCreateRequest{
		SessionUID:  "sess-uid-int",
		ServerUID:   "srv-uid-int",
		Namespace:   "mcp-team-int",
		SessionName: "sess-int",
		CSRSHA256:   csrDigest,
	})
	if err != nil || !created1 {
		t.Fatalf("CreateEnrollmentIntent 1: created=%v err=%v", created1, err)
	}
	en2, created2, err := w.CreateEnrollmentIntent(ctx, apikit.EnrollmentIntentCreateRequest{
		SessionUID:  "sess-uid-int",
		ServerUID:   "srv-uid-int",
		Namespace:   "mcp-team-int",
		SessionName: "sess-int",
		CSRSHA256:   csrDigest,
	})
	if err != nil || created2 || en2.OperationID != en1.OperationID || en2.CertificateRequestName != en1.CertificateRequestName {
		t.Fatalf("CreateEnrollmentIntent 2 not idempotent: created=%v en1=%+v en2=%+v err=%v", created2, en1, en2, err)
	}
}
