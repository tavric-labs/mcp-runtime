package httpx

import "strings"

// NormalizePublicAPIPath strips the public API version prefix so handlers can
// match on /runtime/... paths whether the request arrived as /api/v1/runtime/*
// or legacy /api/runtime/*.
func NormalizePublicAPIPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/"):
		return strings.TrimPrefix(path, "/api/v1")
	case strings.HasPrefix(path, "/api/"):
		return strings.TrimPrefix(path, "/api")
	default:
		return path
	}
}
