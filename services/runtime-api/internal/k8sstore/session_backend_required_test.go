package k8sstore

import (
	"context"
	"errors"
	clienttesting "k8s.io/client-go/testing"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestSessionOperationsRequirePlatformBackend(t *testing.T) {
	for name, call := range map[string]func(*Manager) error{
		"list":     func(m *Manager) error { _, err := m.ListSessions(context.Background(), "team"); return err },
		"get":      func(m *Manager) error { _, err := m.GetSession(context.Background(), "session", "team"); return err },
		"apply":    func(m *Manager) error { _, err := m.ApplySession(context.Background(), &MCPAgentSession{}); return err },
		"revoke":   func(m *Manager) error { return m.RevokeSession(context.Background(), "session", "team") },
		"unrevoke": func(m *Manager) error { return m.UnrevokeSession(context.Background(), "session", "team") },
		"delete":   func(m *Manager) error { return m.DeleteSession(context.Background(), "session", "team") },
		"complete enrollment": func(m *Manager) error {
			return m.CompleteSessionEnrollment(context.Background(), "session", "team", "uid")
		},
		"delete pending": func(m *Manager) error {
			return m.DeletePendingSession(context.Background(), "session", "team", "uid", "human")
		},
		"revoke grant sessions": func(m *Manager) error {
			_, err := m.RevokeGrantSessions(context.Background(), "team", "grant")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			client.PrependReactor("*", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("unexpected Kubernetes session access")
			})
			err := call(NewManager(client, nil))
			if err == nil || !strings.Contains(err.Error(), "platform session backend") {
				t.Fatalf("expected unavailable platform session backend, got %v", err)
			}
			if len(client.Actions()) != 0 {
				t.Fatalf("session operation reached Kubernetes: %v", client.Actions())
			}
		})
	}
}
