package apikit

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// QueryInt parses a required-shape integer query param. Empty uses fallback.
// Non-integer or out-of-range values return *Error with CodeInvalidQueryParam.
func QueryInt(r *http.Request, key string, fallback, minValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, BadRequest(CodeInvalidQueryParam, fmt.Sprintf("%s must be an integer", key))
	}
	if value < minValue || value > maxValue {
		return 0, BadRequest(CodeInvalidQueryParam, fmt.Sprintf("%s must be between %d and %d", key, minValue, maxValue))
	}
	return value, nil
}

// QueryRFC3339Time parses an optional RFC3339 timestamp query param.
func QueryRFC3339Time(r *http.Request, key string) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, BadRequest(CodeInvalidQueryParam, fmt.Sprintf("%s must be RFC3339", key))
	}
	return parsed, nil
}

// QueryIntOr parses an optional integer query param and returns fallback when
// the value is missing, blank or not an integer. It never rejects the request;
// use QueryInt when invalid input must be a 400.
func QueryIntOr(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// ClampInt limits n to the inclusive range [minValue, maxValue].
func ClampInt(n, minValue, maxValue int) int {
	if n < minValue {
		return minValue
	}
	if n > maxValue {
		return maxValue
	}
	return n
}
