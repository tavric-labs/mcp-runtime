// Package apikit is the shared API layer for the platform's JSON services.
//
// It owns the conventions every API endpoint follows:
//
//   - typed, safe errors (Error with BadRequest, NotFound, Conflict and friends)
//     written through WriteError and WriteEnvelope as one standard error body;
//   - pagination (limit and cursor parsing, NextLink, ListMeta);
//   - query and body helpers: QueryInt rejects bad input with a 400, QueryIntOr
//     falls back to a default, ClampInt bounds a value, WriteBodyDecodeError and
//     WriteMethodNotAllowed map common failures to the standard envelope.
//
// It also carries the request and response types of the platform-api endpoints
// that more than one module calls (the /internal/* service-to-service endpoints
// and shared records such as TeamMembership and UserDeactivationResult), so a
// field rename fails to compile on both sides of the split. Those files
// (wire_*.go) hold data shapes only: no handlers, storage or business rules.
//
// Transport construction, TLS and tracing are deliberately not here (see
// transport), and generic response primitives live in httpx.
package apikit
