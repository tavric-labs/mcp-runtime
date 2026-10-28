package platformstore

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.opentelemetry.io/otel/trace"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func contextWithTrace(t *testing.T) context.Context {
	t.Helper()
	traceID, err := trace.TraceIDFromHex(testTraceID)
	if err != nil {
		t.Fatal(err)
	}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), spanContext)
}

func TestWriteAuditStoresTraceIDFromContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("user-1", "grant.create", "g-1", "team-a", "success", "", "127.0.0.1", sqlmock.AnyArg(), "cli", "user_api_key", "", "", "", "", "", testTraceID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	store.WriteAudit(contextWithTrace(t), AuditEvent{
		UserID: "user-1", Action: "grant.create", Resource: "g-1", Namespace: "team-a",
		Status: "success", ActorIP: "127.0.0.1", Source: "cli", AuthIdentity: "user_api_key",
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListAuditLogsFiltersAndReturnsTraceID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	columns := []string{"id", "user_id", "email", "action", "resource", "namespace", "status", "message", "actor_ip", "request_id", "source", "auth_identity", "image_ref", "server_name", "deployment_target", "agent_id", "team_id", "trace_id", "created_at"}
	mock.ExpectQuery(`a\.trace_id = \$1`).
		WithArgs(testTraceID, 50).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(7), "u", "u@example.com", "grant.create", "g-1", "team-a", "success", "", "", "req-1", "cli", "", "", "", "", "", "", testTraceID, time.Unix(0, 0)))

	logs, err := store.ListAuditLogs(context.Background(), OperationsFilter{TraceID: testTraceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].TraceID != testTraceID {
		t.Fatalf("logs = %+v, want one entry with trace id %s", logs, testTraceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
