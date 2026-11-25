package runtimeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeGrantPrimitiveRulesPersistAndSurvivePatch(t *testing.T) {
	mgr := newTestAccessManager(t)
	s := (&RuntimeServer{accessMgr: mgr}).Access()
	apply := func(rules string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleRuntimeGrantApply(w, httptest.NewRequest(http.MethodPost, "/api/runtime/grants", strings.NewReader(
			`{"name":"docs-reader","namespace":"mcp-servers-org","serverRef":{"name":"demo"},"subject":{"humanID":"user-1"},"maxTrust":"low","allowedSideEffects":["read"],"primitiveRules":`+rules+`}`)))
		return w
	}
	for name, bad := range map[string]string{
		"unknown kind":   `[{"kind":"tool","pattern":"x","decision":"allow"}]`,
		"empty pattern":  `[{"kind":"resource","pattern":"","decision":"allow"}]`,
		"mid wildcard":   `[{"kind":"resource","pattern":"a*b","decision":"allow"}]`,
		"bad decision":   `[{"kind":"prompt","pattern":"p","decision":"maybe"}]`,
		"duplicate rule": `[{"kind":"prompt","pattern":"p","decision":"allow"},{"kind":"prompt","pattern":"p","decision":"deny"}]`,
	} {
		if w := apply(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%s", name, w.Code, w.Body.String())
		}
	}
	if w := apply(`[{"kind":"resource","pattern":"file:///docs/*","decision":"allow"},{"kind":"prompt","pattern":"deploy","decision":"deny"}]`); w.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", w.Code, w.Body.String())
	}
	grant, err := mgr.GetGrant(context.Background(), "docs-reader", "mcp-servers-org")
	if err != nil || len(grant.Spec.PrimitiveRules) != 2 || grant.Spec.PrimitiveRules[1].Decision != "deny" {
		t.Fatalf("rules not stored: %#v err=%v", grant, err)
	}
	// Changing the mode must not drop the scoping.
	w := httptest.NewRecorder()
	s.handleGrantPatch(w, httptest.NewRequest(http.MethodPatch, "/api/runtime/grants/mcp-servers-org/docs-reader", strings.NewReader(`{"mode":"monitor"}`)), "mcp-servers-org", "docs-reader")
	if w.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", w.Code, w.Body.String())
	}
	grant, err = mgr.GetGrant(context.Background(), "docs-reader", "mcp-servers-org")
	if err != nil || len(grant.Spec.PrimitiveRules) != 2 {
		t.Fatalf("PATCH dropped primitive rules: %#v err=%v", grant, err)
	}
}
