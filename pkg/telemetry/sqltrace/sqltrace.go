package sqltrace

import (
	"database/sql"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Open is sql.Open with a span per query, parented to the caller's context.
// SQL text and arguments are never recorded; they can carry credentials and
// user data.
func Open(driverName, dsn string) (*sql.DB, error) {
	return otelsql.Open(driverName, dsn,
		otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			DisableQuery:         true,
			OmitConnResetSession: true,
			OmitConnPrepare:      true,
		}),
	)
}
