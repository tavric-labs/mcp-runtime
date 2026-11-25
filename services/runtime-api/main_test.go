package main

import (
	"testing"

	"mcp-runtime-api/internal/platformapi"
)

func TestOptInTeamIntentWorkerAndObserverStayOffByDefault(t *testing.T) {
	t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "")
	t.Setenv("RUNTIME_TEAM_INTENT_OBSERVER", "")

	runner, cancel, err := startOptInTeamIntentWorker(nil, &platformapi.Client{})
	if err != nil || runner != nil || cancel != nil {
		t.Fatalf("expected worker disabled by default, got runner=%v cancel=%v err=%v", runner, cancel, err)
	}

	obsCancel, obsDone, err := startOptInTeamIntentObserver(nil, &platformapi.Client{})
	if err != nil || obsCancel != nil || obsDone != nil {
		t.Fatalf("expected observer disabled by default, got cancel=%v done=%v err=%v", obsCancel, obsDone, err)
	}
}

func TestOptInTeamIntentWorkerRequiresQueueDatabaseURL(t *testing.T) {
	t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "true")
	t.Setenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", "")

	if _, _, err := startOptInTeamIntentWorker(nil, &platformapi.Client{BaseURL: "http://example", Token: "tok"}); err == nil {
		t.Fatal("expected error when RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL is unset")
	}
}
