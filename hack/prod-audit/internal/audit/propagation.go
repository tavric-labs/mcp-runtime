package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// waitForGrantedRead allows bounded propagation of the newly created grant.
// Identity failures, explicit denials and trust failures remain failures.
// A successful probe only starts the full semantic checker; it cannot pass
// acceptance by itself.
func waitForGrantedRead(ctx context.Context, endpoint string) error {
	return poll(ctx, 90*time.Second, func() (bool, error) {
		status, raw, err := request(ctx, http.MethodPost, endpoint, []byte(`{"jsonrpc":"2.0","id":86,"method":"tools/call","params":{"name":"audit_inspect","arguments":{}}}`))
		if err != nil {
			return false, err
		}
		if status == http.StatusOK {
			return true, nil
		}
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &body)
		if status == http.StatusForbidden && body.Error == "tool_not_granted" || status == http.StatusServiceUnavailable && body.Error == "policy_unavailable" {
			return false, nil
		}
		// Do not put arbitrary response text (possibly an upstream page) into
		// reports. These are the recognized non-secret policy reason codes.
		reason := "unrecognized"
		switch body.Error {
		case "session_revoked", "session_expired", "session_not_found", "missing_identity", "missing_session", "tool_denied", "trust_too_low", "side_effect_not_allowed":
			reason = body.Error
		}
		return false, fmt.Errorf("granted read probe rejected: HTTP %d (%s)", status, reason)
	})
}
