package runtimeapi

import (
	"net/http"
	"os"

	"mcp-runtime/pkg/resourcehistory"
	"mcp-runtime/pkg/topology"
)

func (s *RuntimeServer) ResourceHistoryHandler() http.Handler {
	handler := &resourcehistory.Handler{Namespace: topology.PlatformNamespace, PrometheusNamespace: topology.ObservabilityNamespace, IsolatedTelemetry: os.Getenv("GRAFANA_SCOPED_USER_ACCESS") == "true"}
	if s != nil && s.k8sClients != nil {
		handler.Client, handler.Config = s.k8sClients.Clientset, s.k8sClients.Config
	}
	return handler
}
