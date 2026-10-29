package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"mcp-runtime-api/internal/k8sstore"
	"mcp-runtime/pkg/sessionstate"
)

// memorySessionBackend isolates handler fixtures from Kubernetes session storage.
// Real platform persistence and concurrency are covered by platform backend tests.
type memorySessionBackend struct {
	rows      map[string]k8sstore.MCPAgentSession
	deleteErr error
}

func newMemorySessionBackend(items ...*k8sstore.MCPAgentSession) *memorySessionBackend {
	b := &memorySessionBackend{rows: map[string]k8sstore.MCPAgentSession{}}
	for _, item := range items {
		b.rows[item.Namespace+"/"+item.Name] = cloneSession(item)
	}
	return b
}
func cloneSession(s *k8sstore.MCPAgentSession) k8sstore.MCPAgentSession {
	raw, _ := json.Marshal(s)
	var result k8sstore.MCPAgentSession
	_ = json.Unmarshal(raw, &result)
	return result
}
func (b *memorySessionBackend) ListSessions(_ context.Context, namespace string) (*k8sstore.MCPAgentSessionList, error) {
	out := &k8sstore.MCPAgentSessionList{Items: []k8sstore.MCPAgentSession{}}
	for _, s := range b.rows {
		if namespace == "" || s.Namespace == namespace {
			out.Items = append(out.Items, cloneSession(&s))
		}
	}
	return out, nil
}
func (b *memorySessionBackend) GetSession(_ context.Context, name, namespace string) (*k8sstore.MCPAgentSession, error) {
	s, ok := b.rows[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "sessions"}, name)
	}
	copy := cloneSession(&s)
	return &copy, nil
}
func (b *memorySessionBackend) ApplySession(_ context.Context, s *k8sstore.MCPAgentSession) (*k8sstore.MCPAgentSession, error) {
	copy := cloneSession(s)
	key := s.Namespace + "/" + s.Name
	if old, ok := b.rows[key]; ok {
		copy.UID = old.UID
		copy.CreationTimestamp = old.CreationTimestamp
	}
	if copy.UID == "" {
		copy.UID = types.UID("fixture-" + key)
	}
	if copy.CreationTimestamp.IsZero() {
		copy.CreationTimestamp = metav1.Now()
	}
	b.rows[key] = copy
	return b.GetSession(context.Background(), s.Name, s.Namespace)
}
func (b *memorySessionBackend) RevokeSession(ctx context.Context, name, namespace string) error {
	return b.setRevoked(ctx, name, namespace, true)
}
func (b *memorySessionBackend) UnrevokeSession(ctx context.Context, name, namespace string) error {
	return b.setRevoked(ctx, name, namespace, false)
}
func (b *memorySessionBackend) setRevoked(ctx context.Context, name, namespace string, revoked bool) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	s.Spec.Revoked = revoked
	b.rows[namespace+"/"+name] = *s
	return nil
}
func (b *memorySessionBackend) DeleteSession(ctx context.Context, name, namespace string) error {
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
func (b *memorySessionBackend) DeleteSessionVersioned(ctx context.Context, name, namespace, uid string) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(s.UID) != uid {
		return sessionstate.ErrConflict
	}
	return b.DeleteSession(ctx, name, namespace)
}
func (b *memorySessionBackend) CompleteSessionEnrollment(ctx context.Context, name, namespace, uid string) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(s.UID) != uid || s.Spec.Revoked || s.Spec.ExpiresAt == nil || !s.Spec.ExpiresAt.After(time.Now()) {
		return sessionstate.ErrConflict
	}
	delete(s.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
	b.rows[namespace+"/"+name] = *s
	return nil
}
func (b *memorySessionBackend) DeletePendingSession(ctx context.Context, name, namespace, uid, humanID string) error {
	s, err := b.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(s.UID) != uid || humanID == "" || string(s.Spec.Subject.HumanID) != humanID || s.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] != "true" {
		return sessionstate.ErrConflict
	}
	return b.DeleteSession(ctx, name, namespace)
}
func (b *memorySessionBackend) RevokeGrantSessions(ctx context.Context, namespace, name string) (int64, error) {
	var count int64
	for _, s := range b.rows {
		if s.Annotations["mcpruntime.org/access-grant-name"] == name && s.Annotations["mcpruntime.org/access-grant-namespace"] == namespace && !s.Spec.Revoked {
			if err := b.RevokeSession(ctx, s.Name, s.Namespace); err != nil {
				return count, fmt.Errorf("revoke fixture: %w", err)
			}
			count++
		}
	}
	return count, nil
}
