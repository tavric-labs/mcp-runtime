package sqltrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{}, nil }

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func init() { sql.Register("sqltrace-fake", fakeDriver{}) }

func TestOpenRecordsQuerySpanWithoutStatementOrArguments(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	db, err := Open("sqltrace-fake", "postgres://user:secret@db/platform")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, parent := provider.Tracer("test").Start(context.Background(), "request")
	if _, err := db.ExecContext(ctx, "UPDATE users SET token = $1", "hunter2"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	parent.End()

	var found bool
	for _, span := range recorder.Ended() {
		if span.Name() == "request" {
			continue
		}
		found = true
		if span.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Fatalf("span %q is not a child of the request span", span.Name())
		}
		for _, attr := range span.Attributes() {
			value := attr.Value.String()
			if strings.Contains(value, "hunter2") || strings.Contains(value, "UPDATE") || strings.Contains(value, "secret") {
				t.Fatalf("span attribute %s leaks query or credentials: %q", attr.Key, value)
			}
		}
	}
	if !found {
		t.Fatal("no database span recorded")
	}
}
