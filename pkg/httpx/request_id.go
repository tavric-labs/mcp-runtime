package httpx

import (
	"context"
	"regexp"
)

// RequestIDHeader is the request identifier carried on every API call.
const RequestIDHeader = "X-Request-Id"

// requestIDPattern matches the identifier services accept from a caller and
// copy into logs and audit rows. Anything else is rejected so a header cannot
// inject a log line or a stored value.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type requestIDContextKey struct{}

// ValidRequestID reports whether id is safe to echo, log, and store.
func ValidRequestID(id string) bool {
	return requestIDPattern.MatchString(id)
}

// ContextWithRequestID returns a context that carries a validated request id.
// An id that fails ValidRequestID is not stored.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil || !ValidRequestID(id) {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestID returns the validated request id stored in ctx, or "" when absent.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	if !ValidRequestID(id) {
		return ""
	}
	return id
}
