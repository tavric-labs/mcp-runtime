package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	policypkg "mcp-runtime/pkg/policy"
)

func TestPipelineStopsAfterTerminalResponse(t *testing.T) {
	t.Parallel()
	stages := []string{"inspect", "policy", "auth", "authz", "upstream"}
	for _, tc := range []struct {
		name      string
		stop      int
		allowed   bool
		skipAudit bool
	}{
		{name: "inspection rejection", stop: 0},
		{name: "policy rejection", stop: 1},
		{name: "public metadata", stop: 1, allowed: true, skipAudit: true},
		{name: "authentication rejection", stop: 2},
		{name: "authorization rejection", stop: 3},
		{name: "upstream response", stop: 4, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := newExchange(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil), "test")
			policy := testPolicy()
			policyErr := errors.New("snapshot error")
			inspection := inspectedRPC("tools/call", "echo", RPCRequest)
			identity := identityContext{HumanID: "human", AgentID: "agent", TeamID: "team"}
			decision := policypkg.Deny(http.StatusForbidden, "test_denial", "test")
			if tc.allowed {
				decision = policypkg.Allow("test_response", "test")
			}
			var calls []string
			finish := func(stage int) *FilterResponse {
				calls = append(calls, stages[stage])
				if stage != tc.stop {
					return nil
				}
				if tc.skipAudit {
					return &FilterResponse{Decision: decision, kind: responseMetadata, metadata: &oauthMetadataResponse{status: decision.Status}}
				}
				if tc.allowed {
					ex.W.WriteHeader(decision.Status)
					return forwardedResponse(decision)
				}
				return denyResponse(decision)
			}
			pipeline := Pipeline{
				Inspect: FilterFunc[*RequestInput, InspectedRequest](func(in *RequestInput) (InspectedRequest, *FilterResponse) {
					return InspectedRequest{RequestInput: in, Inspection: inspection}, finish(0)
				}),
				Policy: FilterFunc[InspectedRequest, PolicyRequest](func(in InspectedRequest) (PolicyRequest, *FilterResponse) {
					if !reflect.DeepEqual(in.Inspection, inspection) {
						t.Fatal("inspection did not cross policy boundary")
					}
					return PolicyRequest{InspectedRequest: in, Policy: policy, PolicyErr: policyErr}, finish(1)
				}),
				Auth: FilterFunc[PolicyRequest, AuthenticatedRequest](func(in PolicyRequest) (AuthenticatedRequest, *FilterResponse) {
					if in.Policy != policy || in.PolicyErr != policyErr {
						t.Fatal("snapshot did not cross authentication boundary")
					}
					return AuthenticatedRequest{PolicyRequest: in, Caller: verifiedTestCaller(identity)}, finish(2)
				}),
				Authz: FilterFunc[AuthenticatedRequest, EvaluatedRequest](func(in AuthenticatedRequest) (EvaluatedRequest, *FilterResponse) {
					if in.Identity() != identity || in.Policy != policy {
						t.Fatal("identity or snapshot changed before authorization")
					}
					return EvaluatedRequest{AuthenticatedRequest: in, Decision: policypkg.Allow("allowed", "test")}, finish(3)
				}),
				Upstream: FilterFunc[PermittedRequest, EvaluatedRequest](func(permitted PermittedRequest) (EvaluatedRequest, *FilterResponse) {
					in := permitted.request
					if !in.Decision.Allowed || in.Identity() != identity || in.Policy != policy {
						t.Fatal("authorization state did not cross upstream boundary")
					}
					return in, finish(4)
				}),
			}
			pipeline.Run(ex)
			if !reflect.DeepEqual(calls, stages[:tc.stop+1]) {
				t.Fatalf("executed %v, want %v", calls, stages[:tc.stop+1])
			}
			if !reflect.DeepEqual(ex.Decision, decision) || ex.SkipAudit != tc.skipAudit || ex.W.status != decision.Status {
				t.Fatalf("terminal state lost: decision=%+v skipAudit=%v status=%d", ex.Decision, ex.SkipAudit, ex.W.status)
			}
			if !reflect.DeepEqual(ex.Inspection, inspection) {
				t.Fatal("terminal inspection missing from observation record")
			}
			if tc.stop >= 1 && (ex.Policy != policy || ex.PolicyErr != policyErr) {
				t.Fatal("terminal policy snapshot missing from observation record")
			}
			if tc.stop >= 2 && ex.Identity != identity {
				t.Fatal("partial authentication identity missing from observation record")
			}
		})
	}
}

func TestUpstreamRejectsWithoutAllow(t *testing.T) {
	t.Parallel()
	for _, decision := range []policypkg.Decision{
		{},
		policypkg.Deny(http.StatusUnauthorized, "missing_identity", "test"),
	} {
		t.Run(decision.Reason, func(t *testing.T) {
			t.Parallel()
			fixture := newTestExchange(http.MethodGet, "/prefix/mcp", "", map[string]string{
				defaultVerifiedSPIFFEHeader: "assertion",
			})
			fixture.Decision = decision
			s := minimalServer() // No proxy: reaching dispatch would panic.
			s.stripPrefix = "/prefix"
			response := fixture.upstream(s)
			if response == nil || response.Decision.Allowed || fixture.W.status < http.StatusBadRequest {
				t.Fatalf("invalid authorization reached dispatch: response=%+v", response)
			}
			if fixture.R.URL.Path != "/prefix/mcp" || fixture.R.Header.Get(defaultVerifiedSPIFFEHeader) != "assertion" {
				t.Fatal("upstream preparation ran before authorization check")
			}
		})
	}
}

func TestPolicySnapshotSurvivesReloadDuringRequest(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	original := testPolicy()
	s.snapshotPolicy(policySnapshot{Policy: original, Ready: true})
	pipeline := s.buildPipeline()
	pipeline.Auth = FilterFunc[PolicyRequest, AuthenticatedRequest](func(in PolicyRequest) (AuthenticatedRequest, *FilterResponse) {
		s.snapshotPolicy(policySnapshot{Policy: &policypkg.Document{}, Ready: true})
		return AuthenticatedRequest{PolicyRequest: in}, nil
	})
	pipeline.Upstream = FilterFunc[PermittedRequest, EvaluatedRequest](func(permitted PermittedRequest) (EvaluatedRequest, *FilterResponse) {
		in := permitted.request
		if in.Policy != original || !in.Decision.Allowed {
			t.Fatal("request used the replacement policy snapshot")
		}
		return in, forwardedResponse(in.Decision)
	})
	ex := newExchange(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/mcp", nil), "test")
	pipeline.Run(ex)
	if ex.Policy != original || !ex.Decision.Allowed {
		t.Fatal("finalization did not retain the request's original policy")
	}
}
