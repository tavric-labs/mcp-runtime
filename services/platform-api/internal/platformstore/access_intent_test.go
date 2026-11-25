package platformstore

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"mcp-platform-api/internal/accessintent"
	"mcp-runtime/pkg/apikit"
)

func TestStoreAccessIntentMethodsReturnDisabledByDefault(t *testing.T) {
	s := NewForTest(nil, nil)
	ctx := t.Context()

	if _, err := s.ListGrantIntents(ctx); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ListGrantIntents err = %v, want ErrDisabled", err)
	}
	if _, err := s.UpsertGrantIntent(ctx, apikit.GrantIntentUpsertRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("UpsertGrantIntent err = %v, want ErrDisabled", err)
	}
	if _, _, err := s.GetGrantIntent(ctx, "g1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("GetGrantIntent err = %v, want ErrDisabled", err)
	}
	if _, err := s.RevokeGrantIntent(ctx, "g1", false); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("RevokeGrantIntent err = %v, want ErrDisabled", err)
	}
	if _, err := s.ClaimGrantIntentFence(ctx, "g1", "w1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ClaimGrantIntentFence err = %v, want ErrDisabled", err)
	}
	if _, err := s.RecordGrantIntentReceipt(ctx, "g1", apikit.GrantIntentReceiptRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("RecordGrantIntentReceipt err = %v, want ErrDisabled", err)
	}
	if _, err := s.RecordGrantIntentObservation(ctx, "g1", apikit.GrantIntentObservationRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("RecordGrantIntentObservation err = %v, want ErrDisabled", err)
	}
	if _, err := s.ListEnrollmentIntents(ctx); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ListEnrollmentIntents err = %v, want ErrDisabled", err)
	}
	if _, _, err := s.CreateEnrollmentIntent(ctx, apikit.EnrollmentIntentCreateRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("CreateEnrollmentIntent err = %v, want ErrDisabled", err)
	}
	if _, _, err := s.GetEnrollmentIntent(ctx, "op1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("GetEnrollmentIntent err = %v, want ErrDisabled", err)
	}
	if _, err := s.ClaimEnrollmentIntentFence(ctx, "op1", "w1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ClaimEnrollmentIntentFence err = %v, want ErrDisabled", err)
	}
	if _, err := s.RecordEnrollmentIntentReceipt(ctx, "op1", apikit.EnrollmentIntentReceiptRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("RecordEnrollmentIntentReceipt err = %v, want ErrDisabled", err)
	}
	if _, err := s.ActivateEnrollmentIntent(ctx, "op1", apikit.EnrollmentIntentActivateRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ActivateEnrollmentIntent err = %v, want ErrDisabled", err)
	}
	if _, _, err := s.GetAgentCleanupIntent(ctx, "agt-1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("GetAgentCleanupIntent err = %v, want ErrDisabled", err)
	}
	if _, err := s.ClaimAgentCleanupIntentFence(ctx, "agt-1", "w1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ClaimAgentCleanupIntentFence err = %v, want ErrDisabled", err)
	}
	if _, err := s.RecordAgentCleanupIntentReceipt(ctx, "agt-1", apikit.AgentCleanupReceiptRequest{}); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("RecordAgentCleanupIntentReceipt err = %v, want ErrDisabled", err)
	}
	if _, err := s.ClaimAccessIntentObserverLease(ctx, "default", "obs-1"); !errors.Is(err, accessintent.ErrDisabled) {
		t.Fatalf("ClaimAccessIntentObserverLease err = %v, want ErrDisabled", err)
	}
}

func TestSetAgentStatusPreservesSynchronousBehaviorWhenJournalDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := NewStoreWithDB(db)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(int64(1)))
	mock.ExpectQuery(`UPDATE agents SET status=\$2`).
		WithArgs("agt-1", "inactive", "actor-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "team_id", "name", "status", "created_by", "created_at", "updated_at", "deactivated_at", "deactivated_by",
		}).AddRow("agt-1", "team-1", "Runner", "inactive", "creator-1", now, now, now, "actor-1"))
	mock.ExpectQuery(`SELECT data FROM sessions WHERE NOT revoked`).
		WithArgs("agt-1").
		WillReturnRows(sqlmock.NewRows([]string{"data"}))
	mock.ExpectQuery(`SELECT slug FROM teams WHERE id=\$1`).
		WithArgs("team-1").
		WillReturnRows(sqlmock.NewRows([]string{"slug"}).AddRow("alpha"))
	mock.ExpectCommit()

	agent, err := s.SetAgentStatus(t.Context(), "agt-1", "inactive", "actor-1")
	if err != nil {
		t.Fatalf("SetAgentStatus err = %v", err)
	}
	if agent.Status != "inactive" || agent.TeamSlug != "alpha" {
		t.Fatalf("unexpected agent = %+v", agent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
