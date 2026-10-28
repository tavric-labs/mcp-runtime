package platformstore

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"mcp-runtime/pkg/httpx"
)

func TestWriteAuditStoresRequestIDFromContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("user-1", "api_key_create", "uk_1", "user-1", "success", "", "127.0.0.1", "client-req_42.a", "ui", "user_api_key", "", "", "", "", "", "").
		WillReturnResult(sqlmock.NewResult(1, 1))

	ctx := httpx.ContextWithRequestID(context.Background(), "client-req_42.a")
	store.WriteAudit(ctx, AuditEvent{
		UserID: "user-1", Action: "api_key_create", Resource: "uk_1", Namespace: "user-1",
		Status: "success", ActorIP: "127.0.0.1", Source: "ui", AuthIdentity: "user_api_key",
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAuditKeepsRequestIDAlreadyOnTheEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("", "grant.create", "grant-1", "", "success", "", "", "from-caller", "", "", "", "", "", "", "", "").
		WillReturnResult(sqlmock.NewResult(1, 1))

	ctx := httpx.ContextWithRequestID(context.Background(), "internal-hop")
	store.WriteAudit(ctx, AuditEvent{
		Action: "grant.create", Resource: "grant-1", Status: "success", RequestID: "from-caller",
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
