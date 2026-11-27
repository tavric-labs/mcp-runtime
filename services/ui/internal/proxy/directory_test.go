package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTeamAcceptanceProxyPreservesReplayKeyAndRequiresCSRF(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/teams" || r.Header.Get("Idempotency-Key") != "original-replay-key" || r.Header.Get("Authorization") != "Bearer platform-session" {
			t.Errorf("unexpected upstream request: method=%s path=%s replay=%q auth-match=%v", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization") == "Bearer platform-session")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation":{"id":"accepted-operation","state":"Pending"}}`))
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestPlatformSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer platform-session"})
	for _, valid := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/teams", strings.NewReader(`{"slug":"team-a"}`))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		req.Header.Set("Idempotency-Key", "original-replay-key")
		if valid {
			req.Header.Set(CSRFHeaderName, sess.CSRFToken)
		}
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
		want := http.StatusForbidden
		if valid {
			want = http.StatusAccepted
		}
		if w.Code != want {
			t.Fatalf("valid=%v status=%d body=%s", valid, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d", calls)
	}
}
