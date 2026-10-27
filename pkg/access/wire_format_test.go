package access

import (
	"encoding/json"
	"testing"
)

// Wire-format characterization for the apply request contracts shared by the
// CLI and the API. The expected bytes are the pre-split behavior.

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestWireFormatApplyRequests(t *testing.T) {
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
