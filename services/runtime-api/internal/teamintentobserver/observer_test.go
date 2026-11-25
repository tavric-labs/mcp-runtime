package teamintentobserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/kubeworkload"
)

type fakeObserverPlatform struct {
	mu           sync.Mutex
	intents      map[string]apikit.TeamNamespaceIntent
	leaseEpochs  map[string]int64
	observations []apikit.TeamIntentObservationRequest
}

func newFakeObserverPlatform(intents ...apikit.TeamNamespaceIntent) *fakeObserverPlatform {
	m := make(map[string]apikit.TeamNamespaceIntent, len(intents))
	for _, it := range intents {
		m[it.TeamID] = it
	}
	return &fakeObserverPlatform{
		intents:     m,
		leaseEpochs: map[string]int64{},
	}
}

func (f *fakeObserverPlatform) ListTeamIntents(context.Context) ([]apikit.TeamNamespaceIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]apikit.TeamNamespaceIntent, 0, len(f.intents))
	for _, it := range f.intents {
		out = append(out, it)
	}
	return out, nil
}

func (f *fakeObserverPlatform) GetTeamIntent(_ context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.intents[teamID]
	return it, ok, nil
}

func (f *fakeObserverPlatform) ClaimTeamIntentObserverLease(_ context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if clusterKey == "" {
		clusterKey = "default"
	}
	f.leaseEpochs[clusterKey]++
	return apikit.TeamIntentObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: f.leaseEpochs[clusterKey],
	}, nil
}

func (f *fakeObserverPlatform) RecordTeamIntentObservation(_ context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clusterKey := req.ClusterKey
	if clusterKey == "" {
		clusterKey = "default"
	}
	it, ok := f.intents[teamID]
	if !ok {
		return apikit.TeamNamespaceObservation{}, platformapi.ErrTeamNotFound
	}
	if leaseEpoch := f.leaseEpochs[clusterKey]; leaseEpoch > 0 && req.ObserverEpoch < leaseEpoch {
		return apikit.TeamNamespaceObservation{}, platformapi.ErrTeamIntentConflict
	}
	if it.Observation != nil && req.ObserverEpoch < it.Observation.ObserverEpoch {
		return apikit.TeamNamespaceObservation{}, platformapi.ErrTeamIntentConflict
	}
	if req.Health == apikit.TeamIntentHealthReady {
		if it.Deleted || it.Receipt == nil || it.Receipt.AppliedRevisionID != it.DesiredRevisionID || req.ObservedRevisionID != it.Receipt.AppliedRevisionID {
			return apikit.TeamNamespaceObservation{}, platformapi.ErrTeamIntentConflict
		}
		if it.Receipt.NamespaceUID != "" && req.NamespaceUID != it.Receipt.NamespaceUID {
			return apikit.TeamNamespaceObservation{}, platformapi.ErrTeamIntentConflict
		}
	}
	verifiedAt := req.VerifiedAt.UTC()
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	transitionAt := verifiedAt
	if it.Observation != nil && it.Observation.Health == req.Health && it.Observation.Reason == req.Reason {
		transitionAt = it.Observation.LastTransitionAt
	}
	obs := apikit.TeamNamespaceObservation{
		ObserverID:         req.ObserverID,
		ObserverEpoch:      req.ObserverEpoch,
		ObservedRevisionID: req.ObservedRevisionID,
		NamespaceUID:       req.NamespaceUID,
		Health:             req.Health,
		Reason:             req.Reason,
		Message:            req.Message,
		Conditions:         req.Conditions,
		LastTransitionAt:   transitionAt,
		LastVerifiedAt:     verifiedAt,
		UpdatedAt:          verifiedAt,
	}
	it.Observation = &obs
	f.intents[teamID] = it
	f.observations = append(f.observations, req)
	return obs, nil
}

func makePlatformNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform"}}
}

func makeTraefikDeployment(watched ...string) *appsv1.Deployment {
	joined := ""
	for i, w := range watched {
		if i > 0 {
			joined += ","
		}
		joined += w
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "traefik",
						Args: []string{
							"--providers.kubernetesingress.namespaces=" + joined,
							"--providers.kubernetescrd.namespaces=" + joined,
						},
					}},
				},
			},
		},
	}
}

func testTraefikConfig() k8sprovision.TraefikWatchConfig {
	return k8sprovision.TraefikWatchConfig{
		Mode:           "required",
		Namespace:      "traefik",
		Deployment:     "traefik",
		ServiceAccount: "traefik",
	}
}

func provisionCompleteTeamNamespace(t *testing.T, k8s kubernetes.Interface, namespace, teamID, slug, uid string) {
	t.Helper()
	labels := map[string]string{
		kubeworkload.PlatformManagedLabel: "true",
		k8sprovision.TeamIDLabel:          teamID,
		"mcpruntime.org/team-slug":        slug,
		"mcpruntime.org/scope":            "team",
	}
	_, err := k8s.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			UID:    types.UID(uid),
			Labels: labels,
		},
		Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	p := k8sprovision.Provisioner{
		Client: k8s,
		EnsureRegistryPullSecret: func(context.Context, kubernetes.Interface, string) error {
			return nil
		},
	}
	if _, err := p.EnsureOwnedTeamNamespace(t.Context(), namespace, teamID, nil, labels, "traefik"); err != nil {
		t.Fatalf("EnsureOwnedTeamNamespace: %v", err)
	}
	if err := k8sprovision.EnsureTeamTraefikWatch(t.Context(), k8s, namespace, testTraefikConfig()); err != nil {
		t.Fatalf("EnsureTeamTraefikWatch: %v", err)
	}
}

func TestObserverActiveReadyAndDriftRecoveryWithoutMutatingReceipt(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-1", "alpha", "uid-alpha-1")

	appliedAt := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	receiptCopy := apikit.TeamNamespaceReceipt{
		FenceEpoch:        1,
		FenceHolder:       "worker-1",
		AppliedRevisionID: "rev-1",
		Outcome:           apikit.TeamIntentOutcomeApplied,
		NamespaceUID:      "uid-alpha-1",
		Attempt:           1,
		WorkerID:          "worker-1",
		AppliedAt:         &appliedAt,
		UpdatedAt:         appliedAt,
	}
	platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
		Receipt:           &receiptCopy,
	})

	now := appliedAt.Add(time.Minute)
	obs, err := New(Config{
		ObserverID: "observer-1",
		Platform:   platform,
		K8sClient:  k8s,
		Traefik:    testTraefikConfig(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := obs.ClaimLease(t.Context()); err != nil {
		t.Fatalf("ClaimLease() error = %v", err)
	}

	// 1. All prerequisites present -> Ready with matching revision and UID
	if err := obs.ObserveAll(t.Context()); err != nil {
		t.Fatalf("ObserveAll() ready error = %v", err)
	}
	got := platform.intents["team-1"].Observation
	if got == nil || got.Health != apikit.TeamIntentHealthReady || got.ObservedRevisionID != "rev-1" || got.NamespaceUID != "uid-alpha-1" {
		t.Fatalf("initial observation = %+v, want Ready rev-1 uid-alpha-1", got)
	}
	initialTransitionAt := got.LastTransitionAt

	// 2a. Delete platform-default-deny NetworkPolicy -> Degraded (PrerequisiteDrifted) without touching receipt
	if err := k8s.NetworkingV1().NetworkPolicies("mcp-team-alpha").Delete(t.Context(), "platform-default-deny", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete networkpolicy: %v", err)
	}
	now = now.Add(time.Minute)
	if err := obs.ObserveTeam(t.Context(), "team-1"); err != nil {
		t.Fatalf("ObserveTeam() drift np error = %v", err)
	}
	got = platform.intents["team-1"].Observation
	if got == nil || got.Health != apikit.TeamIntentHealthDegraded || got.Reason != ReasonPrerequisiteDrifted {
		t.Fatalf("after deleting netpol observation = %+v, want Degraded/PrerequisiteDrifted", got)
	}
	if *platform.intents["team-1"].Receipt != receiptCopy {
		t.Fatalf("observer mutated receipt: got %+v, want %+v", platform.intents["team-1"].Receipt, receiptCopy)
	}

	// Restore netpol, then delete platform-default-quota ResourceQuota -> still Degraded (PrerequisiteDrifted)
	if err := k8sprovision.EnsureDefaultDenyNetworkPolicy(t.Context(), k8s, "mcp-team-alpha", "traefik"); err != nil {
		t.Fatalf("restore netpol: %v", err)
	}
	if err := k8s.CoreV1().ResourceQuotas("mcp-team-alpha").Delete(t.Context(), "platform-default-quota", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete quota: %v", err)
	}
	now = now.Add(time.Minute)
	if err := obs.ObserveTeam(t.Context(), "team-1"); err != nil {
		t.Fatalf("ObserveTeam() drift quota error = %v", err)
	}
	got = platform.intents["team-1"].Observation
	if got == nil || got.Health != apikit.TeamIntentHealthDegraded || got.Reason != ReasonPrerequisiteDrifted {
		t.Fatalf("after deleting quota observation = %+v, want Degraded/PrerequisiteDrifted", got)
	}

	// Restore quota -> returns to Ready
	p := k8sprovision.Provisioner{
		Client:                   k8s,
		EnsureRegistryPullSecret: func(context.Context, kubernetes.Interface, string) error { return nil },
	}
	if _, err := p.EnsureOwnedTeamNamespace(t.Context(), "mcp-team-alpha", "team-1", nil, nil, "traefik"); err != nil {
		t.Fatalf("restore namespace prerequisites: %v", err)
	}
	now = now.Add(time.Minute)
	if err := obs.ObserveTeam(t.Context(), "team-1"); err != nil {
		t.Fatalf("ObserveTeam() recovered error = %v", err)
	}
	got = platform.intents["team-1"].Observation
	if got == nil || got.Health != apikit.TeamIntentHealthReady || got.ObservedRevisionID != "rev-1" {
		t.Fatalf("after restore observation = %+v, want Ready", got)
	}
	if !got.LastTransitionAt.After(initialTransitionAt) {
		t.Fatalf("expected new LastTransitionAt after Ready->Degraded->Ready recovery")
	}
}

func TestObserverDetectsNamespaceUIDMismatchNeverReady(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	// Namespace recreated with a DIFFERENT UID than receipt recorded
	provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-1", "alpha", "uid-replaced-999")

	platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
		Receipt: &apikit.TeamNamespaceReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-1",
			Outcome:           apikit.TeamIntentOutcomeApplied,
			NamespaceUID:      "uid-original-111",
		},
	})

	obs, err := New(Config{
		ObserverID: "observer-1",
		Platform:   platform,
		K8sClient:  k8s,
		Traefik:    testTraefikConfig(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := obs.ClaimLease(t.Context()); err != nil {
		t.Fatalf("ClaimLease() error = %v", err)
	}
	if err := obs.ObserveTeam(t.Context(), "team-1"); err != nil {
		t.Fatalf("ObserveTeam() error = %v", err)
	}
	got := platform.intents["team-1"].Observation
	if got == nil || got.Health != apikit.TeamIntentHealthDegraded || got.Reason != ReasonNamespaceUIDMismatch {
		t.Fatalf("observation = %+v, want Degraded/NamespaceUIDMismatch", got)
	}
	if got.NamespaceUID != "uid-replaced-999" {
		t.Fatalf("observed namespace_uid = %q, want uid-replaced-999", got.NamespaceUID)
	}
}

func TestObserverPeriodicFreshnessAdvancesVerifiedAtKeepingTransitionConstant(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-1", "alpha", "uid-alpha-1")

	platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
		Receipt: &apikit.TeamNamespaceReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-1",
			Outcome:           apikit.TeamIntentOutcomeApplied,
			NamespaceUID:      "uid-alpha-1",
		},
	})

	t1 := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	now := t1
	obs, err := New(Config{
		ObserverID: "observer-1",
		Platform:   platform,
		K8sClient:  k8s,
		Traefik:    testTraefikConfig(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := obs.ClaimLease(t.Context()); err != nil {
		t.Fatalf("ClaimLease() error = %v", err)
	}
	if err := obs.ObserveAll(t.Context()); err != nil {
		t.Fatalf("first ObserveAll() error = %v", err)
	}
	first := *platform.intents["team-1"].Observation
	if !first.LastVerifiedAt.Equal(t1) || !first.LastTransitionAt.Equal(t1) {
		t.Fatalf("first timestamps: verified=%v transition=%v, want %v", first.LastVerifiedAt, first.LastTransitionAt, t1)
	}

	t2 := t1.Add(30 * time.Second)
	now = t2
	if err := obs.ObserveAll(t.Context()); err != nil {
		t.Fatalf("second ObserveAll() error = %v", err)
	}
	second := *platform.intents["team-1"].Observation
	if !second.LastVerifiedAt.Equal(t2) {
		t.Fatalf("second LastVerifiedAt = %v, want %v", second.LastVerifiedAt, t2)
	}
	if !second.LastTransitionAt.Equal(t1) {
		t.Fatalf("LastTransitionAt changed during steady healthy verification: got %v, want %v", second.LastTransitionAt, t1)
	}
}

func TestObserverStaleEpochStopsWithoutClobberingNewerObserver(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-1", "alpha", "uid-alpha-1")

	platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
		Receipt: &apikit.TeamNamespaceReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-1",
			Outcome:           apikit.TeamIntentOutcomeApplied,
			NamespaceUID:      "uid-alpha-1",
		},
	})

	obsOld, err := New(Config{ObserverID: "observer-old", Platform: platform, K8sClient: k8s, Traefik: testTraefikConfig()})
	if err != nil {
		t.Fatalf("New old: %v", err)
	}
	obsNew, err := New(Config{ObserverID: "observer-new", Platform: platform, K8sClient: k8s, Traefik: testTraefikConfig()})
	if err != nil {
		t.Fatalf("New new: %v", err)
	}

	if err := obsOld.ClaimLease(t.Context()); err != nil {
		t.Fatalf("old ClaimLease: %v", err)
	}
	if err := obsNew.ClaimLease(t.Context()); err != nil {
		t.Fatalf("new ClaimLease: %v", err)
	}
	// Newer observer writes healthy state
	if err := obsNew.ObserveAll(t.Context()); err != nil {
		t.Fatalf("new ObserveAll: %v", err)
	}
	// Stale observer sees a conflict and steps down cleanly without clobbering obsNew
	err = obsOld.ObserveTeam(t.Context(), "team-1")
	if !errors.Is(err, ErrObserverLeaseLost) {
		t.Fatalf("stale observer err = %v, want ErrObserverLeaseLost", err)
	}
	got := platform.intents["team-1"].Observation
	if got == nil || got.ObserverID != "observer-new" || got.ObserverEpoch != 2 {
		t.Fatalf("newer observer state clobbered: %+v", got)
	}
}

func TestObserverDeletedTeamAndSupersededIncarnation(t *testing.T) {
	t.Run("deleted team with Traefik watch removed and namespace retained is Deleted/CleanupComplete", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mcp-team-gone",
					UID:  types.UID("uid-gone"),
					Labels: map[string]string{
						kubeworkload.PlatformManagedLabel: "true",
						k8sprovision.TeamIDLabel:          "team-gone",
					},
				},
			},
			makeTraefikDeployment("mcp-platform"),
		)
		platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
			TeamID:            "team-gone",
			Slug:              "gone",
			Namespace:         "mcp-team-gone",
			DesiredRevisionID: "rev-del-1",
			Deleted:           true,
			ActiveOwnerTeamID: "",
			Receipt: &apikit.TeamNamespaceReceipt{
				FenceEpoch:        2,
				AppliedRevisionID: "rev-del-1",
				Outcome:           apikit.TeamIntentOutcomeCleaned,
			},
		})
		obs, err := New(Config{ObserverID: "observer-1", Platform: platform, K8sClient: k8s, Traefik: testTraefikConfig()})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if err := obs.ClaimLease(t.Context()); err != nil {
			t.Fatalf("ClaimLease(): %v", err)
		}
		if err := obs.ObserveTeam(t.Context(), "team-gone"); err != nil {
			t.Fatalf("ObserveTeam(): %v", err)
		}
		got := platform.intents["team-gone"].Observation
		if got == nil || got.Health != apikit.TeamIntentHealthDeleted || got.Reason != ReasonCleanupComplete || got.ObservedRevisionID != "rev-del-1" {
			t.Fatalf("deleted observation = %+v, want Deleted/CleanupComplete rev-del-1", got)
		}
	})

	t.Run("deleted T1 superseded by recreated T2 observes T1 Deleted/SupersededByNewIncarnation and T2 Ready", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
		provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-2", "alpha", "uid-t2")

		platform := newFakeObserverPlatform(
			apikit.TeamNamespaceIntent{
				TeamID:            "team-1",
				Slug:              "alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-t1-del",
				Deleted:           true,
				ActiveOwnerTeamID: "team-2",
				Receipt: &apikit.TeamNamespaceReceipt{
					FenceEpoch: 2,
					Outcome:    apikit.TeamIntentOutcomeSuperseded,
				},
			},
			apikit.TeamNamespaceIntent{
				TeamID:            "team-2",
				Slug:              "alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-t2-create",
				Deleted:           false,
				ActiveOwnerTeamID: "team-2",
				Receipt: &apikit.TeamNamespaceReceipt{
					FenceEpoch:        1,
					AppliedRevisionID: "rev-t2-create",
					Outcome:           apikit.TeamIntentOutcomeApplied,
					NamespaceUID:      "uid-t2",
				},
			},
		)

		obs, err := New(Config{ObserverID: "observer-1", Platform: platform, K8sClient: k8s, Traefik: testTraefikConfig()})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if err := obs.ClaimLease(t.Context()); err != nil {
			t.Fatalf("ClaimLease(): %v", err)
		}
		if err := obs.ObserveAll(t.Context()); err != nil {
			t.Fatalf("ObserveAll(): %v", err)
		}
		t1Obs := platform.intents["team-1"].Observation
		if t1Obs == nil || t1Obs.Health != apikit.TeamIntentHealthDeleted || t1Obs.Reason != ReasonSupersededByNewIncarnation {
			t.Fatalf("T1 observation = %+v, want Deleted/SupersededByNewIncarnation", t1Obs)
		}
		t2Obs := platform.intents["team-2"].Observation
		if t2Obs == nil || t2Obs.Health != apikit.TeamIntentHealthReady || t2Obs.ObservedRevisionID != "rev-t2-create" || t2Obs.NamespaceUID != "uid-t2" {
			t.Fatalf("T2 observation = %+v, want Ready rev-t2-create uid-t2", t2Obs)
		}
	})
}

func TestObserverRunLoopReactsToInformerAndPeriodicVerification(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	provisionCompleteTeamNamespace(t, k8s, "mcp-team-alpha", "team-1", "alpha", "uid-alpha-1")

	platform := newFakeObserverPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
		Receipt: &apikit.TeamNamespaceReceipt{
			FenceEpoch:        1,
			AppliedRevisionID: "rev-1",
			Outcome:           apikit.TeamIntentOutcomeApplied,
			NamespaceUID:      "uid-alpha-1",
		},
	})

	obs, err := New(Config{
		ObserverID:     "observer-loop",
		Platform:       platform,
		K8sClient:      k8s,
		Traefik:        testTraefikConfig(),
		VerifyInterval: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- obs.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		platform.mu.Lock()
		count := len(platform.observations)
		current := platform.intents["team-1"].Observation
		platform.mu.Unlock()
		if count >= 2 && current != nil && current.Health == apikit.TeamIntentHealthReady {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() exited with error: %v", err)
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.observations) < 2 {
		t.Fatalf("expected initial + periodic observations, got %d", len(platform.observations))
	}
	if got := platform.intents["team-1"].Observation; got == nil || got.Health != apikit.TeamIntentHealthReady {
		t.Fatalf("final observation = %+v, want Ready", got)
	}
}
