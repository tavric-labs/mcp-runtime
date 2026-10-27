package httperrors

import (
	"net/http"

	"mcp-runtime/pkg/apikit"
)

func Unauthorized(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusUnauthorized, apikit.CodeUnauthorized, "unauthorized")
}

func Forbidden(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusForbidden, apikit.CodeForbidden, message)
}

func NotFound(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusNotFound, apikit.CodeNotFound, message)
}

func BadRequest(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidRequestBody, message)
}

func InvalidQuery(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusBadRequest, apikit.CodeInvalidQueryParam, message)
}

func MethodNotAllowed(w http.ResponseWriter, allow string) {
	if allow != "" {
		w.Header().Set("allow", allow)
	}
	apikit.WriteEnvelope(w, http.StatusMethodNotAllowed, apikit.CodeMethodNotAllowed, "method not allowed")
}

func PlatformUnavailable(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "platform identity database not configured")
}

func UserKeyStoreUnavailable(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "user api key store not configured")
}

func OIDCNotConfigured(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "oidc not configured")
}

func AuthFailed(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeAuthFailed, "auth failed")
}

func LoginFailed(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeAuthFailed, "login failed")
}

func QueryFailed(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeQueryFailed, message)
}

func Internal(w http.ResponseWriter, message string) {
	apikit.WriteEnvelope(w, http.StatusInternalServerError, apikit.CodeInternalError, message)
}

func TooManyRequests(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusTooManyRequests, apikit.CodeTooManyRequests, "too many requests")
}

func InvalidCredentials(w http.ResponseWriter) {
	apikit.WriteEnvelope(w, http.StatusUnauthorized, apikit.CodeUnauthorized, "invalid credentials")
}
