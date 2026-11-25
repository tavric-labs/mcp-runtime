package manifest_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// The MCP Server dashboard shows a tenant's own logs. Its query must be exactly
// the selector runtime-api accepts once the variables are substituted: the
// backend rejects anything else, so a drift here silently empties the panel.
func TestGrafanaServerDashboardHasAFixedLogsPanel(t *testing.T) {
	dashboards := grafanaConfigMapNamed(t, "21-grafana-dashboards.yaml", "grafana-dashboards")
	var dash struct {
		Panels []struct {
			ID         int    `json:"id"`
			Type       string `json:"type"`
			Datasource struct {
				UID  string `json:"uid"`
				Type string `json:"type"`
			} `json:"datasource"`
			Targets []struct {
				Expr      string `json:"expr"`
				QueryType string `json:"queryType"`
				MaxLines  int    `json:"maxLines"`
				Direction string `json:"direction"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(dashboards.Data["mcp-server.json"]), &dash); err != nil {
		t.Fatal(err)
	}
	const want = `{namespace="$namespace",app="$server",server_uid="$server_uid"}`
	found := false
	for _, panel := range dash.Panels {
		if panel.Type != "logs" {
			continue
		}
		found = true
		if panel.Datasource.UID != "loki" || panel.Datasource.Type != "loki" {
			t.Errorf("logs panel %d uses datasource %+v, want loki", panel.ID, panel.Datasource)
		}
		if len(panel.Targets) != 1 {
			t.Fatalf("logs panel %d has %d targets, want exactly one fixed query", panel.ID, len(panel.Targets))
		}
		target := panel.Targets[0]
		if target.Expr != want {
			t.Errorf("logs panel expr = %q, want %q", target.Expr, want)
		}
		if target.QueryType != "range" || target.MaxLines <= 0 || target.MaxLines > 500 || strings.TrimSpace(target.Direction) == "" {
			t.Errorf("logs panel query options %+v must be a bounded range query", target)
		}
	}
	if !found {
		t.Fatal("mcp-server dashboard has no logs panel")
	}
}
