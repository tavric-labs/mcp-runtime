package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	policypkg "mcp-runtime/pkg/policy"
)

func evaluatedFor(t *testing.T, decision policypkg.Decision) (EvaluatedRequest, *statusRecorder) {
	t.Helper()
	fixture := newTestExchange(http.MethodPost, "/mcp", "", nil)
	fixture.Decision = decision
	return fixture.EvaluatedRequest, fixture.W
}

// Only an allowed evaluation can become a dispatch input; every other state
// produces a terminal denial with a stable reason and no capability.
func TestPermitRequestIsAllowOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		decision   policypkg.Decision
		wantStatus int
		wantReason string
	}{
		{"zero decision", policypkg.Decision{}, http.StatusForbidden, "undecided"},
		{"denial", policypkg.Deny(http.StatusUnauthorized, "missing_identity", "v1"), http.StatusUnauthorized, "missing_identity"},
		{"denial without reason", policypkg.Decision{Status: http.StatusTeapot}, http.StatusForbidden, "undecided"},
	} {
		evaluated, writer := evaluatedFor(t, tc.decision)
		permitted, response := permitRequest(evaluated, writer)
		if response == nil || response.Decision.Allowed || response.Decision.Status != tc.wantStatus || response.Decision.Reason != tc.wantReason {
			t.Errorf("%s: response=%+v", tc.name, response)
		}
		if permitted.ok() {
			t.Errorf("%s: denied evaluation produced a dispatch capability", tc.name)
		}
	}

	allowed := policypkg.Allow("allowed", "v1")
	evaluated, writer := evaluatedFor(t, allowed)
	if permitted, response := permitRequest(evaluated, writer); response != nil || !permitted.ok() {
		t.Fatalf("allowed evaluation was not permitted: %+v", response)
	}
	if _, response := permitRequest(evaluated, nil); response == nil || response.Decision.Reason != "invalid_dispatch" {
		t.Fatalf("missing writer accepted: %+v", response)
	}
	evaluated.RequestInput = nil
	if _, response := permitRequest(evaluated, writer); response == nil || response.Decision.Reason != "invalid_dispatch" {
		t.Fatalf("missing request accepted: %+v", response)
	}
}

func TestZeroAndHandBuiltCapabilitiesAreNotOK(t *testing.T) {
	t.Parallel()
	if (PermittedRequest{}).ok() {
		t.Fatal("zero capability is ok")
	}
	evaluated, writer := evaluatedFor(t, policypkg.Allow("allowed", "v1"))
	// A capability assembled without permitRequest has no outcome record.
	if (PermittedRequest{request: evaluated, writer: writer}).ok() {
		t.Fatal("capability without an outcome record is ok")
	}
	denied, writer := evaluatedFor(t, policypkg.Deny(http.StatusForbidden, "tool_denied", "v1"))
	if (PermittedRequest{request: denied, writer: writer, outcome: &dispatchOutcome{}}).ok() {
		t.Fatal("capability over a denial is ok")
	}
	s := minimalServer() // no proxy: dispatch would panic
	if _, response := s.upstreamFilter(PermittedRequest{request: denied, writer: &statusRecorder{ResponseWriter: httptest.NewRecorder()}, outcome: &dispatchOutcome{}}); response == nil || response.Decision.Allowed {
		t.Fatalf("upstream dispatched a denied evaluation: %+v", response)
	}
}

// Anonymous/observe traffic and delegated callers stay dispatchable when the
// evaluation allows them; the capability does not require a Runtime identity.
func TestAnonymousAndDelegatedCallersCanBePermitted(t *testing.T) {
	t.Parallel()
	for _, caller := range []Caller{anonymousCaller(), delegatedCaller()} {
		evaluated, writer := evaluatedFor(t, policypkg.Allow("allowed", "v1"))
		evaluated.Caller = caller
		if permitted, response := permitRequest(evaluated, writer); response != nil || !permitted.ok() {
			t.Fatalf("caller %v not permitted: %+v", caller.Kind(), response)
		}
	}
}
