package k8sstore

import (
	"context"
	"fmt"
)

// CompleteSessionEnrollment conditionally completes enrollment in the platform store.
func (m *Manager) CompleteSessionEnrollment(ctx context.Context, name, namespace, uid string) error {
	if m.sessions == nil {
		return errPlatformSessionBackend
	}
	backend, ok := m.sessions.(interface {
		CompleteSessionEnrollment(context.Context, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("conditional enrollment completion is unavailable")
	}
	return backend.CompleteSessionEnrollment(ctx, name, namespace, uid)
}

// DeletePendingSession conditionally removes the caller's pending enrollment.
func (m *Manager) DeletePendingSession(ctx context.Context, name, namespace, uid, humanID string) error {
	if m.sessions == nil {
		return errPlatformSessionBackend
	}
	backend, ok := m.sessions.(interface {
		DeletePendingSession(context.Context, string, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("conditional pending-session cleanup is unavailable")
	}
	return backend.DeletePendingSession(ctx, name, namespace, uid, humanID)
}
