// Package clickhouse is the analytics store client: connection setup, the event model and the dashboard queries.
//
// It builds a ClickHouse connection from Config (address, database, optional
// TLS through transport/tlsconfig), validates database names, and exposes the
// event row model, EventFilters and the dashboard and event-type queries used by
// analytics-api, the processor and the runtime API.
package clickhouse
