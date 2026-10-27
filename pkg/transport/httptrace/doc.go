// Package httptrace instruments existing HTTP clients and RoundTrippers without
// changing their timeouts, redirects, request bodies or connection pools.
// Construction and policy selection belong to the caller; this package neither
// initializes telemetry exporters nor constructs a new network transport.
package httptrace
