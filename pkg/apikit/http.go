package apikit

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const ApplyMaxBytes = 64 * 1024

// WriteBodyDecodeError maps decode failures to the standard error envelope.
func WriteBodyDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		WriteEnvelope(w, http.StatusRequestEntityTooLarge, CodeRequestTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit))
		return
	}
	WriteEnvelope(w, http.StatusBadRequest, CodeInvalidRequestBody, "invalid request body")
}

// WriteMethodNotAllowed sets Allow and returns method_not_allowed.
func WriteMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	if len(methods) > 0 {
		w.Header().Set("Allow", strings.Join(methods, ", "))
	}
	WriteEnvelope(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
}
