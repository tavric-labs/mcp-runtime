package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
)

func TestTeamIntentClientPropagatesRequestIDAndHandlesStatuses(t *testing.T) {
	var seenReqIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqIDs = append(seenReqIDs, r.Header.Get(httpx.RequestIDHeader))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/team-intents":
			_ = json.NewEncoder(w).Encode(apikit.TeamNamespaceIntentListResponse{
				Intents: []apikit.TeamNamespaceIntent{{TeamID: "team-1", Namespace: "mcp-team-alpha"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/internal/team-intents/team-1":
			_ = json.NewEncoder(w).Encode(apikit.TeamNamespaceIntent{TeamID: "team-1", Namespace: "mcp-team-alpha"})
		case r.Method == http.MethodGet && r.URL.Path == "/internal/team-intents/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/internal/team-intents/team-1/fence":
			_ = json.NewEncoder(w).Encode(apikit.TeamIntentFenceResponse{
				FenceEpoch: 3,
				Intent:     apikit.TeamNamespaceIntent{TeamID: "team-1", DesiredRevisionID: "rev-1"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/internal/team-intents/team-1/receipt":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodPost && r.URL.Path == "/internal/team-intents/observer-lease":
			_ = json.NewEncoder(w).Encode(apikit.TeamIntentObserverLeaseResponse{
				ClusterKey: "default", ObserverID: "obs-1", ObserverEpoch: 2,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/internal/team-intents/team-1/observation":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := &Client{BaseURL: server.URL, Token: "internal-token"}
	ctx := httpx.ContextWithRequestID(context.Background(), "req-intent-42")

	items, err := c.ListTeamIntents(ctx)
	if err != nil || len(items) != 1 || items[0].TeamID != "team-1" {
		t.Fatalf("ListTeamIntents() = %+v, %v", items, err)
	}
	item, found, err := c.GetTeamIntent(ctx, "team-1")
	if err != nil || !found || item.TeamID != "team-1" {
		t.Fatalf("GetTeamIntent(team-1) = %+v, %v, %v", item, found, err)
	}
	_, found, err = c.GetTeamIntent(ctx, "missing")
	if err != nil || found {
		t.Fatalf("GetTeamIntent(missing) = found=%v, err=%v", found, err)
	}
	fence, err := c.ClaimTeamIntentFence(ctx, "team-1", "worker-1")
	if err != nil || fence.FenceEpoch != 3 {
		t.Fatalf("ClaimTeamIntentFence() = %+v, %v", fence, err)
	}
	if _, err := c.RecordTeamIntentReceipt(ctx, "team-1", apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: "rev-1", FenceEpoch: 2, Outcome: apikit.TeamIntentOutcomeApplied, WorkerID: "worker-1",
	}); !errors.Is(err, ErrTeamIntentConflict) {
		t.Fatalf("RecordTeamIntentReceipt() err = %v, want ErrTeamIntentConflict", err)
	}
	lease, err := c.ClaimTeamIntentObserverLease(ctx, "default", "obs-1")
	if err != nil || lease.ObserverEpoch != 2 {
		t.Fatalf("ClaimTeamIntentObserverLease() = %+v, %v", lease, err)
	}
	if _, err := c.RecordTeamIntentObservation(ctx, "team-1", apikit.TeamIntentObservationRequest{
		ObserverID: "obs-1", ObserverEpoch: 2, Health: apikit.TeamIntentHealthReady,
	}); !errors.Is(err, ErrTeamIntentDisabled) {
		t.Fatalf("RecordTeamIntentObservation() err = %v, want ErrTeamIntentDisabled", err)
	}

	if len(seenReqIDs) != 7 {
		t.Fatalf("seenReqIDs count = %d, want 7", len(seenReqIDs))
	}
	for i, got := range seenReqIDs {
		if got != "req-intent-42" {
			t.Fatalf("request %d X-Request-Id = %q, want req-intent-42", i, got)
		}
	}
}
