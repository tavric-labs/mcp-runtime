package access

import (
	"encoding/json"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

// Characterization of the JSON and unstructured wire format of grants,
// sessions, their summaries and the apply requests. The pure package split must
// not change a single byte of this output, so the expected values below are the
// pre-split behavior and are the oracle for the split.

// Inputs deliberately cover a non-UTC offset, fractional seconds, a zero time
// and an absent time, because metav1.Time normalizes each of these.
const grantInput = `{
  "apiVersion": "mcpruntime.org/v1alpha1",
  "kind": "MCPAccessGrant",
  "metadata": {"name": "g1", "namespace": "mcp-servers", "creationTimestamp": "2026-10-27T10:00:00+05:30", "labels": {"a": "b"}},
  "spec": {
    "mode": "enforce",
    "serverRef": {"name": "srv", "namespace": "mcp-servers"},
    "subject": {"humanID": "h1", "teamID": "t1"},
    "maxTrust": "medium",
    "allowedSideEffects": ["read"],
    "policyVersion": "v1",
    "expiresAt": "2026-10-27T10:00:00.987+05:30",
    "toolRules": [{"name": "echo", "decision": "deny"}]
  },
  "status": {"phase": "Active", "conditions": [
    {"type": "Ready", "status": "True", "observedGeneration": 3, "lastTransitionTime": "2026-10-27T10:00:00.5Z", "reason": "Ok", "message": "fine"},
    {"type": "Stale", "status": "False", "lastTransitionTime": null, "reason": "None", "message": ""}
  ]}
}`

const grantWant = `{"kind":"MCPAccessGrant","apiVersion":"mcpruntime.org/v1alpha1","metadata":{"name":"g1","namespace":"mcp-servers","creationTimestamp":"2026-10-27T04:30:00Z","labels":{"a":"b"}},"spec":{"mode":"enforce","serverRef":{"name":"srv","namespace":"mcp-servers"},"subject":{"humanID":"h1","teamID":"t1"},"maxTrust":"medium","allowedSideEffects":["read"],"policyVersion":"v1","expiresAt":"2026-10-27T04:30:00Z","toolRules":[{"name":"echo","decision":"deny"}]},"status":{"phase":"Active","conditions":[{"type":"Ready","status":"True","observedGeneration":3,"lastTransitionTime":"2026-10-27T10:00:00Z","reason":"Ok","message":"fine"},{"type":"Stale","status":"False","lastTransitionTime":null,"reason":"None","message":""}]}}`

const sessionInput = `{
  "apiVersion": "mcpruntime.org/v1alpha1",
  "kind": "MCPAgentSession",
  "metadata": {"name": "s1", "namespace": "mcp-servers", "creationTimestamp": "2026-10-27T10:00:00Z", "annotations": {"mcpruntime.org/server-uid": "u1"}},
  "spec": {"serverRef": {"name": "srv"}, "subject": {"humanID": "h1", "agentID": "a1"}, "consentedTrust": "low", "revoked": true,
           "upstreamTokenSecretRef": {"name": "sec", "key": "k"}, "policyVersion": "v1"},
  "status": {"phase": "Revoked"}
}`

const sessionWant = `{"kind":"MCPAgentSession","apiVersion":"mcpruntime.org/v1alpha1","metadata":{"name":"s1","namespace":"mcp-servers","creationTimestamp":"2026-10-27T10:00:00Z","annotations":{"mcpruntime.org/server-uid":"u1"}},"spec":{"serverRef":{"name":"srv"},"subject":{"humanID":"h1","agentID":"a1"},"consentedTrust":"low","revoked":true,"upstreamTokenSecretRef":{"name":"sec","key":"k"},"policyVersion":"v1"},"status":{"phase":"Revoked"}}`

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestWireFormatGrantJSONRoundTrip(t *testing.T) {
	var grant MCPAccessGrant
	if err := json.Unmarshal([]byte(grantInput), &grant); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, grant); got != grantWant {
		t.Fatalf("grant JSON changed:\n got  %s\n want %s", got, grantWant)
	}
}

func TestWireFormatSessionJSONRoundTrip(t *testing.T) {
	var session MCPAgentSession
	if err := json.Unmarshal([]byte(sessionInput), &session); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, session); got != sessionWant {
		t.Fatalf("session JSON changed:\n got  %s\n want %s", got, sessionWant)
	}
}

// The store reads objects through the unstructured converter; the typed value
// that comes out of it must serialize to the same JSON as a direct decode.
func TestWireFormatUnstructuredConversionMatchesJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		into  func() any
	}{
		"grant":   {grantInput, func() any { return &MCPAccessGrant{} }},
		"session": {sessionInput, func() any { return &MCPAgentSession{} }},
	} {
		t.Run(name, func(t *testing.T) {
			var content map[string]any
			if err := json.Unmarshal([]byte(tc.input), &content); err != nil {
				t.Fatal(err)
			}
			viaConverter := tc.into()
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, viaConverter); err != nil {
				t.Fatal(err)
			}
			direct := tc.into()
			if err := json.Unmarshal([]byte(tc.input), direct); err != nil {
				t.Fatal(err)
			}
			if got, want := jsonOf(t, viaConverter), jsonOf(t, direct); got != want {
				t.Fatalf("unstructured conversion differs from JSON decode:\n got  %s\n want %s", got, want)
			}
			back, err := runtime.DefaultUnstructuredConverter.ToUnstructured(viaConverter)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip := tc.into()
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(back, roundTrip); err != nil {
				t.Fatal(err)
			}
			// Sub-second precision is intentionally dropped on the way out, so the
			// stable contract is the wire form, not the in-memory value.
			if got, want := jsonOf(t, roundTrip), jsonOf(t, viaConverter); got != want {
				t.Fatalf("ToUnstructured/FromUnstructured is not stable:\n got  %s\n want %s", got, want)
			}
		})
	}
}

// pinLocalZone fixes time.Local so results do not depend on the machine. The
// summaries format creationTimestamp in local time but append a literal "Z", so
// "age" is only correct when the process runs in UTC. That latent behavior is
// preserved here on purpose; fixing it is a separate, user-approved change.
func pinLocalZone(t *testing.T) {
	t.Helper()
	previous := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	t.Cleanup(func() { time.Local = previous })
}

func TestWireFormatSummariesAndApplyRequests(t *testing.T) {
	pinLocalZone(t)
	var grant MCPAccessGrant
	if err := json.Unmarshal([]byte(grantInput), &grant); err != nil {
		t.Fatal(err)
	}
	const wantGrantSummary = `{"mode":"enforce","name":"g1","namespace":"mcp-servers","serverRef":{"name":"srv","namespace":"mcp-servers"},"subject":{"humanID":"h1","teamID":"t1"},"maxTrust":"medium","allowedSideEffects":["read"],"expiresAt":"2026-10-27T04:30:00Z","disabled":false,"age":"2026-10-27T10:00:00Z"}`
	if got := jsonOf(t, ToGrantSummary(grant)); got != wantGrantSummary {
		t.Fatalf("grant summary changed:\n got  %s\n want %s", got, wantGrantSummary)
	}

	var session MCPAgentSession
	if err := json.Unmarshal([]byte(sessionInput), &session); err != nil {
		t.Fatal(err)
	}
	const wantSessionSummary = `{"name":"s1","namespace":"mcp-servers","serverRef":{"name":"srv"},"subject":{"humanID":"h1","agentID":"a1"},"consentedTrust":"low","revoked":true,"age":"2026-10-27T15:30:00Z","policyVersion":"v1","serverUID":"u1"}`
	if got := jsonOf(t, ToSessionSummary(session)); got != wantSessionSummary {
		t.Fatalf("session summary changed:\n got  %s\n want %s", got, wantSessionSummary)
	}

	var grantReq GrantApplyRequest
	const grantReqInput = `{"name":"g","namespace":"n","serverRef":{"name":"s"},"subject":{"humanID":"h"},"maxTrust":"low","allowedSideEffects":[],"policyVersion":"v1","expiresAt":"2026-10-27T10:00:00+05:30","toolRules":[]}`
	if err := json.Unmarshal([]byte(grantReqInput), &grantReq); err != nil {
		t.Fatal(err)
	}
	const wantGrantReq = `{"name":"g","namespace":"n","serverRef":{"name":"s"},"subject":{"humanID":"h"},"maxTrust":"low","allowedSideEffects":[],"policyVersion":"v1","expiresAt":"2026-10-27T04:30:00Z","toolRules":[]}`
	if got := jsonOf(t, grantReq); got != wantGrantReq {
		t.Fatalf("grant apply request changed:\n got  %s\n want %s", got, wantGrantReq)
	}

	var sessionReq SessionApplyRequest
	if err := json.Unmarshal([]byte(`{"name":"s","namespace":"n","serverRef":{"name":"x"},"subject":{"humanID":"h"},"consentedTrust":"low","expiresAt":null,"policyVersion":"v1"}`), &sessionReq); err != nil {
		t.Fatal(err)
	}
	const wantSessionReq = `{"name":"s","namespace":"n","serverRef":{"name":"x"},"subject":{"humanID":"h"},"consentedTrust":"low","expiresAt":null,"policyVersion":"v1"}`
	if got := jsonOf(t, sessionReq); got != wantSessionReq {
		t.Fatalf("session apply request changed:\n got  %s\n want %s", got, wantSessionReq)
	}
}
