package runtimeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func lokiTestExpr(namespace, name, uid string) string {
	return fmt.Sprintf(`{namespace=%q,app=%q,server_uid=%q}`, namespace, name, uid)
}

func lokiTestBody(expr, queryType string, maxLines int, extra string) string {
	return fmt.Sprintf(`{"from":"%d","to":"%d","queries":[{"refId":"A","datasource":{"uid":"loki","type":"loki"},"expr":%q,"queryType":%q,"maxLines":%d,"direction":"backward","datasourceId":99,"url":"http://foreign"%s}]}`,
		time.Now().Add(-time.Hour).UnixMilli(), time.Now().UnixMilli(), expr, queryType, maxLines, extra)
}

func TestTenantGrafanaLokiQueryBoundary(t *testing.T) {
	t.Setenv("GRAFANA_SCOPED_USER_ACCESS", "true")
	owned := ownedTestMCPServer("demo", "mcp-servers-org", "alice")
	foreign := ownedTestMCPServer("foreign", "mcp-servers-org", "bob")
	team := ownedTestMCPServer("team", "mcp-team-acme", "bob")
	server := newRuntimeServerWithMCPServers(t, owned, foreign, team)
	p := principal{Role: roleUser, Subject: "alice", Namespace: "alice", AllowedNamespaces: []string{"alice", "mcp-servers-org", "mcp-team-acme"}, Teams: []principalTeam{{Namespace: "mcp-team-acme", Role: "member"}}}
	mine := lokiTestExpr(owned.Namespace, owned.Name, string(owned.UID))
	theirs := lokiTestExpr(foreign.Namespace, foreign.Name, string(foreign.UID))
	teams := lokiTestExpr(team.Namespace, team.Name, string(team.UID))

	var sawQuery map[string]any
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		sawQuery = raw["queries"].([]any)[0].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":{"A":{"frames":[]}}}`)
	}))
	defer upstream.Close()
	t.Setenv("GRAFANA_UPSTREAM", upstream.URL)

	for _, tc := range []struct {
		name, body string
		p          principal
		want       int
	}{
		{"owner", lokiTestBody(mine, "range", 100, ""), p, 200},
		{"team member", lokiTestBody(teams, "range", 100, ""), p, 200},
		{"empty query type means range", lokiTestBody(mine, "", 100, ""), p, 200},
		{"someone else's server", lokiTestBody(theirs, "range", 100, ""), p, 403},
		{"team server without membership", lokiTestBody(teams, "range", 100, ""), principal{Role: roleUser, Subject: "other", Namespace: "other"}, 403},
		{"reused name, old instance", lokiTestBody(lokiTestExpr(owned.Namespace, owned.Name, "old-instance"), "range", 100, ""), p, 403},
		{"regex broadens the app", lokiTestBody(strings.Replace(mine, `app="demo"`, `app=~".+"`, 1), "range", 100, ""), p, 403},
		{"regex broadens the namespace", lokiTestBody(strings.Replace(mine, `namespace="mcp-servers-org"`, `namespace=~".+"`, 1), "range", 100, ""), p, 403},
		{"missing uid", lokiTestBody(strings.Replace(mine, `,server_uid="`+string(owned.UID)+`"`, "", 1), "range", 100, ""), p, 403},
		{"line filter appended", lokiTestBody(mine+` |= "token"`, "range", 100, ""), p, 403},
		{"pipeline appended", lokiTestBody(mine+` | json | line_format "{{.x}}"`, "range", 100, ""), p, 403},
		{"second selector unioned", lokiTestBody(mine+` or `+theirs, "range", 100, ""), p, 403},
		{"metric query over a stream", lokiTestBody(`rate(`+mine+`[5m])`, "range", 100, ""), p, 403},
		{"selector with a trailing comment", lokiTestBody(mine+" # "+theirs, "range", 100, ""), p, 403},
		{"instant query type", lokiTestBody(mine, "instant", 100, ""), p, 403},
		{"stats query type", lokiTestBody(mine, "stats", 100, ""), p, 403},
		{"wrong datasource type", strings.Replace(lokiTestBody(mine, "range", 100, ""), `"type":"loki"`, `"type":"prometheus"`, 1), p, 403},
		{"tempo is still admin only", strings.Replace(strings.Replace(lokiTestBody(mine, "range", 100, ""), `"uid":"loki"`, `"uid":"tempo"`, 1), `"type":"loki"`, `"type":"tempo"`, 1), p, 403},
		{"unknown datasource", strings.Replace(lokiTestBody(mine, "range", 100, ""), `"uid":"loki"`, `"uid":"other"`, 1), p, 403},
		{"empty expr", lokiTestBody("", "range", 100, ""), p, 403},
		{"direction must be known", strings.Replace(lokiTestBody(mine, "range", 100, ""), `"direction":"backward"`, `"direction":"sideways"`, 1), p, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := requests
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/observability/grafana/api/ds/query", strings.NewReader(tc.body))
			r = r.WithContext(withPrincipal(r.Context(), tc.p))
			w := httptest.NewRecorder()
			server.HandleRuntimeGrafana(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want != 200 && requests != before {
				t.Fatal("a rejected Loki query reached the shared Grafana datasource")
			}
		})
	}

	t.Run("accepted query is reconstructed, not forwarded as sent", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/observability/grafana/api/ds/query", strings.NewReader(lokiTestBody(mine, "range", 100000, `,"editorMode":"code","legendFormat":"{{pod}}"`)))
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		server.HandleRuntimeGrafana(w, r)
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
		if sawQuery["datasourceId"] != nil || sawQuery["url"] != nil || sawQuery["editorMode"] != nil {
			t.Errorf("unsafe options survived: %v", sawQuery)
		}
		if sawQuery["maxLines"] != float64(500) {
			t.Errorf("maxLines = %v, want it clamped to 500", sawQuery["maxLines"])
		}
		if sawQuery["queryType"] != "range" || sawQuery["expr"] != mine {
			t.Errorf("query = %v", sawQuery)
		}
	})

	t.Run("a batch is authorized per target across datasources", func(t *testing.T) {
		var batch grafanaQueryRequest
		if err := json.Unmarshal([]byte(lokiTestBody(mine, "range", 100, "")), &batch); err != nil {
			t.Fatal(err)
		}
		prom := grafanaQuery{RefID: "B", Datasource: grafanaDataSource{UID: "prometheus", Type: "prometheus"}, Expr: scopedPrometheusQueries(owned.Namespace, owned.Name, string(owned.UID))[0].Query, Range: true}
		batch.Queries = append(batch.Queries, prom)
		data, _ := json.Marshal(batch)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/observability/grafana/api/ds/query", strings.NewReader(string(data)))
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		server.HandleRuntimeGrafana(w, r)
		if w.Code != 200 {
			t.Fatalf("own logs plus own metrics rejected: %d %s", w.Code, w.Body.String())
		}
		batch.Queries = append(batch.Queries, grafanaQuery{RefID: "C", Datasource: grafanaDataSource{UID: "loki", Type: "loki"}, Expr: theirs, QueryType: "range"})
		data, _ = json.Marshal(batch)
		r = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/observability/grafana/api/ds/query", strings.NewReader(string(data)))
		r = r.WithContext(withPrincipal(r.Context(), p))
		w = httptest.NewRecorder()
		server.HandleRuntimeGrafana(w, r)
		if w.Code != 403 {
			t.Fatalf("a foreign Loki query hidden in a batch was accepted: %d", w.Code)
		}
	})
}

func TestTenantGrafanaLokiFeedsAndRoutes(t *testing.T) {
	t.Setenv("GRAFANA_SCOPED_USER_ACCESS", "true")
	server := newRuntimeServerWithMCPServers(t, ownedTestMCPServer("demo", "mcp-servers-org", "alice"))
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	t.Setenv("GRAFANA_UPSTREAM", upstream.URL)
	p := principal{Role: roleUser, Subject: "alice", Namespace: "alice", AllowedNamespaces: []string{"alice", "mcp-servers-org"}}
	do := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/runtime/observability/grafana"+path, nil)
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		server.HandleRuntimeGrafana(w, r)
		return w
	}
	// Label and index feeds are answered locally and never read the global store.
	for _, path := range []string{
		"/api/datasources/uid/loki/resources/labels",
		"/api/datasources/uid/loki/resources/label/app/values",
		"/api/datasources/uid/loki/resources/detected_labels",
		"/api/datasources/uid/loki/resources/index/stats",
		"/api/datasources/uid/loki/resources/index/volume",
	} {
		before := requests
		if w := do(http.MethodGet, path); w.Code != 200 || requests != before {
			t.Errorf("%s: status=%d, upstream hit=%v", path, w.Code, requests != before)
		}
	}
	if w := do(http.MethodGet, "/api/datasources/uid/loki"); w.Code != 200 {
		t.Errorf("datasource settings: status=%d", w.Code)
	}
	// Reading the settings is not editing them.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		before := requests
		if w := do(method, "/api/datasources/uid/loki"); w.Code != 403 || requests != before {
			t.Errorf("%s /api/datasources/uid/loki: status=%d, upstream hit=%v", method, w.Code, requests != before)
		}
	}
	// Everything that could carry an arbitrary query stays closed.
	for _, path := range []string{
		"/api/datasources/proxy/uid/loki/loki/api/v1/query_range",
		"/api/datasources/uid/loki/resources/query_range",
		"/api/datasources/uid/loki/resources/series",
		"/api/datasources/uid/tempo",
		"/api/datasources/uid/tempo/resources/search",
		"/api/traces/abcdef",
		"/api/plugins/loki/settings",
	} {
		before := requests
		if w := do(http.MethodGet, path); w.Code != 403 || requests != before {
			t.Errorf("%s: status=%d, upstream hit=%v, want a 403 that never reaches Grafana", path, w.Code, requests != before)
		}
	}
}
