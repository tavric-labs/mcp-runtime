package runtimeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testTraceID = "0af7651916cd43dd8448eb211c80319c"

func resourceBlock(uid, service string, spans ...string) string {
	attrs := fmt.Sprintf(`{"key":"service.name","value":{"stringValue":%q}}`, service)
	if uid != "" {
		attrs += fmt.Sprintf(`,{"key":"mcp.server.uid","value":{"stringValue":%q}}`, uid)
	}
	return fmt.Sprintf(`{"resource":{"attributes":[%s]},"scopeSpans":[{"spans":[%s]}]}`, attrs, strings.Join(spans, ","))
}

func spanJSON(id, parent, name string) string {
	return fmt.Sprintf(`{"traceId":%q,"spanId":%q,"parentSpanId":%q,"name":%q,"kind":2,"startTimeUnixNano":"1000","endTimeUnixNano":"2000","attributes":[{"key":"mcp.method.name","value":{"stringValue":"tools/call"}}],"status":{}}`, testTraceID, id, parent, name)
}

type tempoFake struct {
	searchQuery string
	calls       int
	traceBody   string
	status      int
}

func (f *tempoFake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/search":
			f.searchQuery = r.URL.Query().Get("q")
			fmt.Fprintf(w, `{"traces":[{"traceID":%q,"rootServiceName":"another-tenant-gateway","rootTraceName":"secret-root-operation","startTimeUnixNano":"5","durationMs":99,
				"spanSets":[{"matched":1,"spans":[{"spanID":"aaaaaaaaaaaaaaaa","name":"POST /mcp","startTimeUnixNano":"1000","durationNanos":"3000000","attributes":[{"key":"mcp.method.name","value":{"stringValue":"tools/call"}}]}]}]},
				{"traceID":"1111111111111111111111111111111b","rootServiceName":"x","rootTraceName":"y"}]}`, testTraceID)
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			fmt.Fprint(w, f.traceBody)
		default:
			t.Errorf("unexpected Tempo path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func tracesRequest(server *RuntimeServer, p principal, method string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/runtime/observability/traces?"+values.Encode(), nil)
	r = r.WithContext(withPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	server.HandleRuntimeObservabilityTraces(w, r)
	return w
}

func TestTenantTracesAreScopedToTheServersOwnSpans(t *testing.T) {
	owned := ownedTestMCPServer("demo", "mcp-servers-org", "alice")
	foreign := ownedTestMCPServer("foreign", "mcp-servers-org", "bob")
	server := newRuntimeServerWithMCPServers(t, owned, foreign)
	alice := principal{Role: roleUser, Subject: "alice", Namespace: "alice", AllowedNamespaces: []string{"alice", "mcp-servers-org"}}
	uid := string(owned.UID)
	tempo := &tempoFake{}
	srv := httptest.NewServer(tempo.handler(t))
	defer srv.Close()
	t.Setenv("TEMPO_API_URL", srv.URL)
	own := url.Values{"namespace": {owned.Namespace}, "server": {owned.Name}}

	t.Run("search uses a server-built TraceQL and returns no foreign root names", func(t *testing.T) {
		w := tracesRequest(server, alice, http.MethodGet, own)
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
		want := fmt.Sprintf(`{resource.service.namespace="mcp-servers-org" && resource.mcp.server.uid=%q}`, uid)
		if tempo.searchQuery != want {
			t.Errorf("TraceQL = %q, want %q", tempo.searchQuery, want)
		}
		body := w.Body.String()
		for _, leaked := range []string{"another-tenant-gateway", "secret-root-operation", "rootServiceName", "rootTraceName", "1111111111111111111111111111111b"} {
			if strings.Contains(body, leaked) {
				t.Errorf("search response leaked %q: %s", leaked, body)
			}
		}
		var got struct {
			Traces []struct {
				TraceID string `json:"traceID"`
				Name    string `json:"name"`
			} `json:"traces"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Traces) != 1 || got.Traces[0].TraceID != testTraceID || got.Traces[0].Name != "POST /mcp" {
			t.Errorf("traces = %+v (%v)", got, err)
		}
	})

	t.Run("a trace shows only this server's spans", func(t *testing.T) {
		tempo.traceBody = `{"batches":[` +
			resourceBlock(uid, "demo-gateway", spanJSON("aaaaaaaaaaaaaaaa", "", "POST /mcp"), spanJSON("bbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaa", "tools/call echo")) + `,` +
			resourceBlock(string(foreign.UID), "foreign-gateway", spanJSON("cccccccccccccccc", "aaaaaaaaaaaaaaaa", "FOREIGN-SPAN-NAME")) + `,` +
			resourceBlock("old-instance", "demo-gateway", spanJSON("dddddddddddddddd", "", "REUSED-NAME-SPAN")) + `,` +
			resourceBlock("", "platform-caller", spanJSON("eeeeeeeeeeeeeeee", "", "UNLABELLED-CALLER-SPAN")) + `]}`
		w := tracesRequest(server, alice, http.MethodGet, url.Values{"namespace": {owned.Namespace}, "server": {owned.Name}, "trace_id": {testTraceID}})
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, leaked := range []string{"FOREIGN-SPAN-NAME", "REUSED-NAME-SPAN", "UNLABELLED-CALLER-SPAN", "foreign-gateway", "platform-caller", string(foreign.UID), "cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee"} {
			if strings.Contains(body, leaked) {
				t.Errorf("trace response leaked %q: %s", leaked, body)
			}
		}
		var got struct {
			TraceID string `json:"traceID"`
			Spans   []struct {
				SpanID  string `json:"spanID"`
				Name    string `json:"name"`
				Service string `json:"service"`
			} `json:"spans"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.TraceID != testTraceID || len(got.Spans) != 2 {
			t.Fatalf("spans = %+v (%v)", got, err)
		}
		if got.Spans[0].Name != "POST /mcp" || got.Spans[0].Service != "demo-gateway" || got.Spans[1].Name != "tools/call echo" {
			t.Errorf("spans = %+v", got.Spans)
		}
	})

	t.Run("the newer resourceSpans shape is filtered the same way", func(t *testing.T) {
		tempo.traceBody = `{"trace":{"resourceSpans":[` + resourceBlock(uid, "demo-gateway", spanJSON("aaaaaaaaaaaaaaaa", "", "ok-span")) + `,` + resourceBlock(string(foreign.UID), "f", spanJSON("cccccccccccccccc", "", "FOREIGN-SPAN-NAME")) + `]}}`
		w := tracesRequest(server, alice, http.MethodGet, url.Values{"namespace": {owned.Namespace}, "server": {owned.Name}, "trace_id": {testTraceID}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "ok-span") || strings.Contains(w.Body.String(), "FOREIGN-SPAN-NAME") {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a trace with none of this server's spans is not found", func(t *testing.T) {
		tempo.traceBody = `{"batches":[` + resourceBlock(string(foreign.UID), "f", spanJSON("cccccccccccccccc", "", "FOREIGN-SPAN-NAME")) + `]}`
		w := tracesRequest(server, alice, http.MethodGet, url.Values{"namespace": {owned.Namespace}, "server": {owned.Name}, "trace_id": {testTraceID}})
		if w.Code != 404 || strings.Contains(w.Body.String(), "FOREIGN") {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a server the caller cannot observe never reaches Tempo", func(t *testing.T) {
		before := tempo.calls
		for _, values := range []url.Values{
			{"namespace": {foreign.Namespace}, "server": {foreign.Name}},
			{"namespace": {foreign.Namespace}, "server": {foreign.Name}, "trace_id": {testTraceID}},
			{"namespace": {"nope"}, "server": {"nope"}},
		} {
			if w := tracesRequest(server, alice, http.MethodGet, values); w.Code != 404 {
				t.Errorf("%v: status=%d", values, w.Code)
			}
		}
		if tempo.calls != before {
			t.Error("an unauthorized request reached Tempo")
		}
	})

	t.Run("malformed input is rejected before Tempo", func(t *testing.T) {
		before := tempo.calls
		for _, id := range []string{"../etc/passwd", "zz", strings.Repeat("a", 33), "0af7651916cd43dd8448eb211c80319c/../x", ""} {
			values := url.Values{"namespace": {owned.Namespace}, "server": {owned.Name}, "trace_id": {id}}
			if id == "" {
				continue
			}
			if w := tracesRequest(server, alice, http.MethodGet, values); w.Code != 400 {
				t.Errorf("trace_id %q: status=%d", id, w.Code)
			}
		}
		if tempo.calls != before {
			t.Error("a malformed trace id reached Tempo")
		}
		if w := tracesRequest(server, alice, http.MethodPost, own); w.Code != 405 {
			t.Errorf("POST status=%d", w.Code)
		}
	})

	t.Run("an unavailable Tempo is a bad gateway, not a leak", func(t *testing.T) {
		tempo.status = http.StatusInternalServerError
		defer func() { tempo.status = 0 }()
		if w := tracesRequest(server, alice, http.MethodGet, own); w.Code != 502 {
			t.Errorf("status=%d", w.Code)
		}
	})
}
