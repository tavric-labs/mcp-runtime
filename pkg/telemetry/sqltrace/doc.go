// Package sqltrace opens database/sql handles whose queries join the active
// trace. Spans carry the operation and database system but never SQL text or
// arguments.
package sqltrace
