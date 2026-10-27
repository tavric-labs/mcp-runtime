package svcboot

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var generatedRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Every response names its request, so a client or an operator can quote it, and
// the handler (and its audit events) see the same identifier.
func TestWithRequestID(t *testing.T) {
	cases := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{name: "generated when absent"},
		{name: "kept when well formed", incoming: "client-req_42.a", keep: true},
		{name: "replaced when it carries control or oversize data", incoming: strings.Repeat("a", 129)},
		{name: "replaced when it has unsafe characters", incoming: "bad id\r\nX-Injected: 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Get("X-Request-Id")
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.incoming != "" {
				req.Header["X-Request-Id"] = []string{tc.incoming}
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-Id")
			if got == "" || got != seen {
				t.Fatalf("response id %q, handler saw %q; want one shared id", got, seen)
			}
			if tc.keep && got != tc.incoming {
				t.Fatalf("id = %q, want the client's %q", got, tc.incoming)
			}
			if !tc.keep && !generatedRequestID.MatchString(got) {
				t.Fatalf("id = %q, want a generated 32-hex id", got)
			}
		})
	}
}
