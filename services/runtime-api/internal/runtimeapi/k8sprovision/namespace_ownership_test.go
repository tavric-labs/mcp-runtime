package k8sprovision

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/pkg/kubeworkload"
)

func noopPullSecret(context.Context, kubernetes.Interface, string) error { return nil }

func teamNamespaceLabels(teamID, slug string) map[string]string {
	return map[string]string{
		kubeworkload.PlatformManagedLabel: "true",
		TeamIDLabel:                       teamID,
		"mcpruntime.org/team-slug":        slug,
		"mcpruntime.org/scope":            "team",
	}
}

func TestEnsureOwnedTeamNamespaceCreatesAndIsIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	p := Provisioner{Client: client, EnsureRegistryPullSecret: noopPullSecret}

	uid, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-alpha", "team-1", nil, teamNamespaceLabels("team-1", "alpha"))
	if err != nil {
		t.Fatalf("first EnsureOwnedTeamNamespace() = %v", err)
	}
	ns, err := client.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Labels[TeamIDLabel] != "team-1" || ns.Labels[kubeworkload.PlatformManagedLabel] != "true" {
		t.Fatalf("unexpected labels: %+v", ns.Labels)
	}
	if uid != string(ns.UID) {
		t.Fatalf("uid = %q, want %q", uid, ns.UID)
	}

	// Idempotent retry for the same team succeeds
	if _, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-alpha", "team-1", nil, teamNamespaceLabels("team-1", "alpha")); err != nil {
		t.Fatalf("second EnsureOwnedTeamNamespace() = %v", err)
	}
}

func TestEnsureOwnedTeamNamespaceRejectsForeignOrDifferentActiveTeam(t *testing.T) {
	t.Run("unmanaged foreign namespace", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "mcp-team-foreign",
				UID:    types.UID("uid-foreign"),
				Labels: map[string]string{"env": "custom"},
			},
		})
		p := Provisioner{Client: client, EnsureRegistryPullSecret: noopPullSecret}
		_, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-foreign", "team-1", nil, teamNamespaceLabels("team-1", "foreign"))
		if !errors.Is(err, ErrNamespaceOwnershipConflict) {
			t.Fatalf("err = %v, want ErrNamespaceOwnershipConflict", err)
		}
		ns, _ := client.CoreV1().Namespaces().Get(t.Context(), "mcp-team-foreign", metav1.GetOptions{})
		if ns.Labels[TeamIDLabel] != "" {
			t.Fatalf("foreign namespace was mutated: %+v", ns.Labels)
		}
	})

	t.Run("owned by newer incarnation", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "mcp-team-alpha",
				UID:    types.UID("uid-t2"),
				Labels: teamNamespaceLabels("team-2", "alpha"),
			},
		})
		p := Provisioner{Client: client, EnsureRegistryPullSecret: noopPullSecret}
		// Stale team-1 does not have team-2 in its adoptablePriorTeamIDs
		_, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-alpha", "team-1", nil, teamNamespaceLabels("team-1", "alpha"))
		if !errors.Is(err, ErrNamespaceOwnershipConflict) {
			t.Fatalf("stale team-1 err = %v, want ErrNamespaceOwnershipConflict", err)
		}
		ns, _ := client.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
		if ns.Labels[TeamIDLabel] != "team-2" {
			t.Fatalf("newer team-2 label was clobbered: %+v", ns.Labels)
		}
	})
}

func TestEnsureOwnedTeamNamespaceAdoptsPriorDeletedIncarnationAndRejectsTerminating(t *testing.T) {
	t.Run("adopts soft-deleted prior incarnation", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "mcp-team-alpha",
				UID:    types.UID("uid-retained"),
				Labels: teamNamespaceLabels("team-1", "alpha"),
			},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		})
		p := Provisioner{Client: client, EnsureRegistryPullSecret: noopPullSecret}
		uid, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-alpha", "team-2", []string{"team-1"}, teamNamespaceLabels("team-2", "alpha"))
		if err != nil {
			t.Fatalf("adopt prior deleted incarnation err = %v", err)
		}
		if uid != "uid-retained" {
			t.Fatalf("uid = %q, want uid-retained", uid)
		}
		ns, _ := client.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
		if ns.Labels[TeamIDLabel] != "team-2" {
			t.Fatalf("label = %q, want team-2", ns.Labels[TeamIDLabel])
		}
	})

	t.Run("rejects terminating namespace as retryable", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "mcp-team-term",
				UID:    types.UID("uid-term"),
				Labels: teamNamespaceLabels("team-1", "term"),
			},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		})
		p := Provisioner{Client: client, EnsureRegistryPullSecret: noopPullSecret}
		_, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-term", "team-1", nil, teamNamespaceLabels("team-1", "term"))
		if !errors.Is(err, ErrNamespaceTerminating) {
			t.Fatalf("err = %v, want ErrNamespaceTerminating", err)
		}
	})
}
