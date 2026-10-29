package k8sstore

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"mcp-runtime/pkg/sessionstate"
)

// retirementSessionBackend supplies platform-session fixtures for cleanup tests.
// Other session operations are intentionally not implemented by this fixture.
type retirementSessionBackend struct {
	SessionBackend
	rows      map[string]MCPAgentSession
	deleteErr error
}

func newMemorySessionBackend(items ...*MCPAgentSession) *retirementSessionBackend {
	b := &retirementSessionBackend{rows: map[string]MCPAgentSession{}}
	for _, item := range items {
		b.rows[item.Namespace+"/"+item.Name] = *item
	}
	return b
}
func (b *retirementSessionBackend) ListSessions(_ context.Context, namespace string) (*MCPAgentSessionList, error) {
	out := &MCPAgentSessionList{Items: []MCPAgentSession{}}
	for _, s := range b.rows {
		if namespace == "" || s.Namespace == namespace {
			out.Items = append(out.Items, s)
		}
	}
	return out, nil
}
func (b *retirementSessionBackend) GetSession(_ context.Context, name, namespace string) (*MCPAgentSession, error) {
	s, ok := b.rows[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "sessions"}, name)
	}
	return &s, nil
}
func (b *retirementSessionBackend) RevokeSession(ctx context.Context, name, namespace string) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	s.Spec.Revoked = true
	b.rows[namespace+"/"+name] = *s
	return nil
}
func (b *retirementSessionBackend) DeleteSession(ctx context.Context, name, namespace string) error {
	if b.deleteErr != nil {
		err := b.deleteErr
		b.deleteErr = nil
		return err
	}
	if _, err := b.GetSession(ctx, name, namespace); err != nil {
		return err
	}
	delete(b.rows, namespace+"/"+name)
	return nil
}
func (b *retirementSessionBackend) DeleteSessionVersioned(ctx context.Context, name, namespace, uid string) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(s.UID) != uid {
		return sessionstate.ErrConflict
	}
	return b.DeleteSession(ctx, name, namespace)
}
