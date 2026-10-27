package svcboot

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"mcp-runtime/pkg/httpx"
)

// WithRequestID gives every request an identifier and returns it in the
// X-Request-Id response header. A well-formed identifier from the caller is kept
// so a trace can span services; anything else is replaced, never echoed, so the
// header cannot carry injected data. The same identifier is stored on the
// request context, which is where audit writes and access logs read it.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(httpx.RequestIDHeader)
		if !httpx.ValidRequestID(id) {
			id = newRequestID()
		}
		r.Header.Set(httpx.RequestIDHeader, id)
		w.Header().Set(httpx.RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(httpx.ContextWithRequestID(r.Context(), id)))
	})
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a fixed marker
		// still lets the response carry an identifier.
		return "unavailable"
	}
	return hex.EncodeToString(raw[:])
}
