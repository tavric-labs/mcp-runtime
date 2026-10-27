package svcboot

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
)

const requestIDHeader = "X-Request-Id"

var wellFormedRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// WithRequestID gives every request an identifier and returns it in the
// X-Request-Id response header. A well-formed identifier from the caller is kept
// so a trace can span services; anything else is replaced, never echoed, so the
// header cannot carry injected data. The identifier is also set on the request,
// which is where handlers and audit events read it.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !wellFormedRequestID.MatchString(id) {
			id = newRequestID()
		}
		r.Header.Set(requestIDHeader, id)
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r)
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
