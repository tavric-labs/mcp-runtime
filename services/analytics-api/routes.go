package main

import (
	"net/http"

	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/platformauth"
)

func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	auth := s.authentic.Middleware
	admin := func(h http.Handler) http.Handler {
		return auth(s.authentic.RequireRole(platformauth.RoleAdmin, h))
	}

	registerV1 := func(pattern string, handler http.Handler) {
		mux.Handle("/api/v1"+pattern, handler)
	}

	registerV1("/openapi.yaml", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		openapi.ServeYAML(w, openAPISpec)
	}))

	registerV1("/events", admin(http.HandlerFunc(s.events.Events)))
	registerV1("/stats", admin(http.HandlerFunc(s.events.Stats)))
	registerV1("/sources", admin(http.HandlerFunc(s.events.Sources)))
	registerV1("/event-types", admin(http.HandlerFunc(s.events.EventTypes)))
	registerV1("/analytics/usage", admin(http.HandlerFunc(s.usage.HandleAdminUsage)))
	registerV1("/user/analytics/usage", auth(http.HandlerFunc(s.usage.HandleUserUsage)))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil || s.usage.DB == nil {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "clickhouse not configured")
		return
	}
	if err := s.usage.DB.Ping(r.Context()); err != nil {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "clickhouse unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
