package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"mcp-runtime-api/admin"
	"mcp-runtime-api/deployments"
	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	runtimehandlers "mcp-runtime-api/runtime"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/platformauth"
)

func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	auth := s.authentic.Middleware
	adminOnly := func(h http.Handler) http.Handler {
		return auth(s.authentic.RequireRole(platformauth.RoleAdmin, h))
	}

	register := func(pattern string, handler http.Handler) {
		mux.Handle("/api/v1"+pattern, handler)
	}

	register("/openapi.yaml", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		openapi.ServeYAML(w, openAPISpec)
	}))

	if s.runtime == nil {
		return
	}
	// This route has its own exact Prometheus ServiceAccount/audience
	// TokenReview boundary; platform sessions and API keys never authorize it.
	mux.Handle("/internal/observability/resources/metrics", s.runtime.ResourceHistoryHandler())
	// Complete the canonical old-policy replacement before this API replica
	// becomes ready. A partial/failed migration must never report healthy.
	policyContext, cancelPolicies := context.WithTimeout(context.Background(), 30*time.Second)
	policyErr := s.runtime.Deployments().ReconcileTeamNamespaceNetworkPolicies(policyContext)
	cancelPolicies()
	if policyErr != nil {
		log.Printf("workload network policy reconciliation failed: %v", policyErr)
		s.runtimeInit = "workload network policies unavailable"
	}
	routes := runtimeRoutes{
		runtime:   s.runtime,
		platform:  s.platform,
		auth:      auth,
		adminOnly: adminOnly,
		mount:     register,
	}
	routes.registerRoutes(mux)
}

type runtimeRoutes struct {
	runtime   *runtimeapi.RuntimeServer
	platform  *platformapi.Client
	auth      func(http.Handler) http.Handler
	adminOnly func(http.Handler) http.Handler
	mount     func(string, http.Handler)
}

func (rr runtimeRoutes) registerRoutes(mux *http.ServeMux) {
	runtimeServer := rr.runtime
	runtimeServer.SetAuditWriter(rr.platform)
	deploymentService := runtimeServer.Deployments()
	accessService := runtimeServer.Access()
	inventoryService := runtimeServer.Inventory()
	registryPushService := runtimeServer.RegistryPush()
	rr.mount("/runtime/workspace/teams", rr.auth(http.HandlerFunc(runtimeServer.HandleWorkspaceTeams)))
	rr.mount("/runtime/workspace/servers", rr.auth(http.HandlerFunc(runtimeServer.HandleWorkspaceServers)))
	rr.mount("/runtime/workspace/access", rr.auth(http.HandlerFunc(runtimeServer.HandleWorkspaceAccess)))
	rr.mount("/admin/overview/directory", rr.adminOnly(http.HandlerFunc(runtimeServer.HandleOverviewDirectory)))
	rr.mount("/admin/overview/servers", rr.adminOnly(http.HandlerFunc(runtimeServer.HandleOverviewServers)))

	rr.mount("/dashboard/summary", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleDashboardSummary(runtimeServer, w, r)
	})))
	rr.mount("/runtime/servers", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServers(runtimeServer, w, r)
	})))
	rr.mount("/runtime/tools", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTools(inventoryService, w, r)
	})))
	rr.mount("/runtime/servers/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServerItem(runtimeServer, w, r)
	})))
	rr.mount("/runtime/server-events", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServerEvents(runtimeServer, w, r)
	})))
	rr.mount("/runtime/observability/links", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityLinks)))
	rr.mount("/runtime/observability/prometheus/query", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityPrometheusQuery)))
	rr.mount("/runtime/observability/metrics", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityMetrics)))
	rr.mount("/runtime/observability/logs/query", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityLogs)))
	rr.mount("/runtime/observability/grafana/", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeGrafana)))
	rr.mount("/runtime/teams", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTeams(runtimeServer, w, r)
	})))
	rr.mount("/runtime/delegation-recipients", rr.auth(http.HandlerFunc(runtimeServer.HandleDelegationRecipients)))
	rr.mount("/runtime/teams/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTeamItemPath(runtimeServer, w, r)
	})))
	rr.mount("/runtime/agents/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeServer.HandleRuntimeAgentPath(w, r)
	})))
	rr.mount("/runtime/namespaces", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeNamespaces(runtimeServer, w, r)
	})))
	rr.mount("/runtime/namespaces/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeNamespaceItem(runtimeServer, w, r)
	})))
	rr.mount("/deployments", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleDeployments(deploymentService, w, r)
	})))
	rr.mount("/deployments/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleDeploymentItem(deploymentService, w, r)
	})))
	rr.mount("/admin/operations", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.HandleOperations(w, r, admin.Dependencies{
			Platform:  rr.platform,
			Runtime:   runtimeServer,
			WriteJSON: httpx.WriteJSON,
		})
	})))
	rr.mount("/admin/deployments", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleAdminDeployments(deploymentService, w, r)
	})))
	rr.mount("/runtime/grants", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeGrants(accessService, w, r)
	})))
	rr.mount("/runtime/sessions", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeSessions(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/sessions", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterSession(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/sessions/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterSessionItemPath(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/preflight", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterPreflight(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/certificates", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterCertificate(accessService, w, r)
	})))
	rr.mount("/runtime/registry/push", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeRegistryPush(registryPushService, w, r)
	})))
	mux.HandleFunc("/internal/registry-push/tar", registryPushService.HandleRegistryPushTransfer)
	rr.mount("/runtime/components", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeComponents(runtimeServer, w, r)
	})))
	rr.mount("/runtime/policy", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimePolicy(accessService, w, r)
	})))
	rr.mount("/runtime/actions/restart", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleActionRestart(runtimeServer, w, r)
	})))
	rr.mount("/runtime/grants/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleGrantItemPath(accessService, w, r)
	})))
	rr.mount("/runtime/sessions/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleSessionItemPath(accessService, w, r)
	})))
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if s.runtimeInit != "" {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.runtimeInit != "" {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "runtime unavailable")
		return
	}
	if s.runtime == nil || !s.runtime.KubernetesAvailable() {
		apikit.WriteEnvelope(w, http.StatusServiceUnavailable, apikit.CodeServiceUnavailable, "kubernetes not available")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
