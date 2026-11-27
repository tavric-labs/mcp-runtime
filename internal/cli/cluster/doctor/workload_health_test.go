package doctor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
)

func TestWorkloadHealthDistinguishesHistoricRestartsFromCurrentFailures(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name     string
		ready    bool
		restarts int
		waiting  string
		finished time.Time
		want     bool
	}{
		{"healthy never restarted", true, 0, "", time.Time{}, true},
		{"historic restarts recovered", true, 72, "", now.Add(-time.Hour), true},
		{"recent repeated restarts", true, 3, "", now.Add(-time.Minute), false},
		{"restart time missing", true, 3, "", time.Time{}, false},
		{"not ready despite historic restart", false, 72, "", now.Add(-time.Hour), false},
		{"crash loop", false, 1, "CrashLoopBackOff", now.Add(-time.Hour), false},
		{"image pull failure", false, 0, "ImagePullBackOff", time.Time{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := map[string]any{"running": map[string]any{"startedAt": now.Add(-time.Hour)}}
			if tt.waiting != "" {
				state = map[string]any{"waiting": map[string]string{"reason": tt.waiting}}
			}
			container := map[string]any{"name": "postgres", "ready": tt.ready, "restartCount": tt.restarts, "state": state}
			if !tt.finished.IsZero() {
				container["lastState"] = map[string]any{"terminated": map[string]any{"finishedAt": tt.finished}}
			}
			pods, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "mcp-postgres-0"}, "status": map[string]any{"containerStatuses": []any{container}}}}})
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				args := strings.Join(spec.Args, " ")
				switch {
				case strings.Contains(args, "get namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-platform")}
				case strings.Contains(args, "get deploy"):
					return &core.MockCommand{OutputData: []byte(`{"items":[{"metadata":{"name":"mcp-platform-api"},"spec":{"replicas":1},"status":{"readyReplicas":1,"updatedReplicas":1,"availableReplicas":1}}]}`)}
				case strings.Contains(args, "get pods"):
					return &core.MockCommand{OutputData: pods}
				default:
					t.Fatalf("unexpected command: %s", args)
					return &core.MockCommand{RunErr: fmt.Errorf("unexpected command")}
				}
			}}
			got := checkPlatformWorkloadHealth(core.NewTestKubectlClient(mock))
			if got.OK != tt.want {
				t.Fatalf("OK=%v want=%v detail=%s", got.OK, tt.want, got.Detail)
			}
		})
	}
}
