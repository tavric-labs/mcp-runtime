package agentadapter

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// These are proxy interoperability checks, not claims that Runtime implements
// an upstream server's protocol negotiation or legacy SSE session endpoint.
func TestReleaseProtocolTransportMatrix(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		for _, format := range []string{"application/json", "text/event-stream"} {
			for _, mode := range []string{"certificate", "header"} {
				for _, status := range []int{http.StatusOK, http.StatusForbidden} {
					t.Run(fmt.Sprintf("%s/%s/%s/%d", version, format, mode, status), func(t *testing.T) {
						t.Parallel()
						body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":%q}}}`, version)
						session := "upstream-session"
						if version == "2026-07-28" {
							session = ""
						}
						upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							got, err := io.ReadAll(r.Body)
							if err != nil || string(got) != body || r.Header.Get(MCPProtocolHeader) != version || r.Header.Get(MCPSessionHeader) != session {
								t.Errorf("request changed across adapter: headers=%v body=%s err=%v", r.Header, got, err)
							}
							if r.Header.Get("Authorization") != "Bearer test-credential" {
								t.Error("adapter did not preserve authentication")
							}
							if version == "2026-07-28" && r.Header.Get("Mcp-Method") != "tools/list" {
								t.Error("per-request method metadata was lost")
							}
							w.Header().Set("Content-Type", format)
							w.Header().Set(MCPSessionHeader, session)
							w.WriteHeader(status)
							if format == "text/event-stream" {
								fmt.Fprint(w, "data: ")
							}
							fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
							if format == "text/event-stream" {
								fmt.Fprint(w, "\n\n")
								w.(http.Flusher).Flush()
							}
						}))
						defer upstream.Close()
						target, _ := url.Parse(upstream.URL + "/server/mcp")
						cfg := testConfig(target)
						cfg.Transport = &RuntimeTransport{Base: upstream.Client().Transport}
						if mode == "header" {
							cfg.AuthMode = AuthModeHeader
							cfg.CertificateIdentity = false
						}
						handler, err := NewHTTPProxyHandler(cfg)
						if err != nil {
							t.Fatal(err)
						}
						req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json, text/event-stream")
						req.Header.Set("Authorization", "Bearer test-credential")
						req.Header.Set(MCPProtocolHeader, version)
						req.Header.Set(MCPSessionHeader, session)
						req.Header.Set("Mcp-Method", "tools/list")
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, req)
						if rec.Code != status || rec.Header().Get("Content-Type") != format || rec.Header().Get(MCPSessionHeader) != session || !strings.Contains(rec.Body.String(), `"tools":[]`) {
							t.Fatalf("response changed: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
						}
					})
				}
			}
		}
	}
}
