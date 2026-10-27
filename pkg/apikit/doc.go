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
// Transport construction, TLS and tracing are deliberately not here (see
// transport), and generic response primitives live in httpx.
package apikit
