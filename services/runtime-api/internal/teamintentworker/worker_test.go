package teamintentworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
	"mcp-runtime/pkg/kubeworkload"
)

type fakeIntentPlatform struct {
	mu           sync.Mutex
	intents      map[string]apikit.TeamNamespaceIntent
	fenceEpochs  map[string]int64
	receipts     []apikit.TeamIntentReceiptRequest
	seenReqIDs   []string
	onGetIntent  func(teamID string)
	receiptError error
}

func newFakeIntentPlatform(intents ...apikit.TeamNamespaceIntent) *fakeIntentPlatform {
	m := map[string]apikit.TeamNamespaceIntent{}
	for _, it := range intents {
		m[it.TeamID] = it
	}
	return &fakeIntentPlatform{
		intents:     m,
		fenceEpochs: map[string]int64{},
	}
}

func (f *fakeIntentPlatform) ClaimTeamIntentFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id := httpx.RequestID(ctx); id != "" {
		f.seenReqIDs = append(f.seenReqIDs, id)
	}
	it, ok := f.intents[teamID]
	if !ok {
		return apikit.TeamIntentFenceResponse{}, platformapi.ErrTeamNotFound
	}
	f.fenceEpochs[teamID]++
	epoch := f.fenceEpochs[teamID]
	it.Receipt = &apikit.TeamNamespaceReceipt{
		FenceEpoch:  epoch,
		FenceHolder: workerID,
		UpdatedAt:   time.Now().UTC(),
	}
	f.intents[teamID] = it
	return apikit.TeamIntentFenceResponse{FenceEpoch: epoch, Intent: it}, nil
}

func (f *fakeIntentPlatform) GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error) {
	if f.onGetIntent != nil {
		f.onGetIntent(teamID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if id := httpx.RequestID(ctx); id != "" {
		f.seenReqIDs = append(f.seenReqIDs, id)
	}
	it, ok := f.intents[teamID]
	return it, ok, nil
}

func (f *fakeIntentPlatform) RecordTeamIntentReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id := httpx.RequestID(ctx); id != "" {
		f.seenReqIDs = append(f.seenReqIDs, id)
	}
	f.receipts = append(f.receipts, req)
	if f.receiptError != nil {
		return apikit.TeamNamespaceReceipt{}, f.receiptError
	}
	it, ok := f.intents[teamID]
	if !ok {
		return apikit.TeamNamespaceReceipt{}, platformapi.ErrTeamNotFound
	}
	if req.FenceEpoch != f.fenceEpochs[teamID] || req.DesiredRevisionID != it.DesiredRevisionID {
		return apikit.TeamNamespaceReceipt{}, platformapi.ErrTeamIntentConflict
	}
	if req.Outcome == apikit.TeamIntentOutcomeApplied && (it.Deleted || it.ActiveOwnerTeamID != teamID) {
		return apikit.TeamNamespaceReceipt{}, platformapi.ErrTeamIntentConflict
	}
	if req.Outcome == apikit.TeamIntentOutcomeCleaned && (!it.Deleted || it.ActiveOwnerTeamID != "") {
		return apikit.TeamNamespaceReceipt{}, platformapi.ErrTeamIntentConflict
	}
	now := time.Now().UTC()
	rec := apikit.TeamNamespaceReceipt{
		FenceEpoch:     req.FenceEpoch,
		FenceHolder:    req.WorkerID,
		Outcome:        req.Outcome,
		NamespaceUID:   req.NamespaceUID,
		Attempt:        req.Attempt,
		WorkerID:       req.WorkerID,
		LastErrorClass: req.LastErrorClass,
		LastError:      req.LastError,
		UpdatedAt:      now,
	}
	if req.Outcome == apikit.TeamIntentOutcomeApplied || req.Outcome == apikit.TeamIntentOutcomeCleaned {
		rec.AppliedRevisionID = req.DesiredRevisionID
		rec.AppliedAt = &now
	}
	it.Receipt = &rec
	f.intents[teamID] = it
	return rec, nil
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

func newTestK8sReconciler(client kubernetes.Interface) *K8sNamespaceReconciler {
	return &K8sNamespaceReconciler{
		Client: client,
		EnsureRegistryPullSecret: func(context.Context, kubernetes.Interface, string) error {
			return nil
		},
		Traefik: k8sprovision.TraefikWatchConfig{
			Mode:           "required",
			Namespace:      "traefik",
			Deployment:     "traefik",
			ServiceAccount: "traefik",
		},
	}
}

func newRiverJob(args ReconcileJobArgs, attempt, maxAttempts int) *river.Job[ReconcileJobArgs] {
	return &river.Job[ReconcileJobArgs]{
		JobRow: &rivertype.JobRow{
			ID:          42,
			Attempt:     attempt,
			MaxAttempts: maxAttempts,
			Kind:        args.Kind(),
			Queue:       QueueName,
		},
		Args: args,
	}
}

func TestWorkerProvisionsNamespaceAndRecordsFencedAppliedReceiptWithTrace(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Name:              "Alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-1",
		ActiveOwnerTeamID: "team-1",
	})
	w := NewWorker("worker-1", platform, newTestK8sReconciler(k8s))

	job := newRiverJob(ReconcileJobArgs{
		TeamID:      "team-1",
		RevisionID:  "rev-1",
		RequestID:   "req-team-create-1",
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}, 1, 8)

	if err := w.Work(t.Context(), job); err != nil {
		t.Fatalf("Work() error = %v", err)
	}

	ns, err := k8s.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected namespace created: %v", err)
	}
	if ns.Labels[k8sprovision.TeamIDLabel] != "team-1" {
		t.Fatalf("team-id label = %q, want team-1", ns.Labels[k8sprovision.TeamIDLabel])
	}
	traefik, _ := k8s.AppsV1().Deployments("traefik").Get(t.Context(), "traefik", metav1.GetOptions{})
	if arg := traefik.Spec.Template.Spec.Containers[0].Args[0]; arg != "--providers.kubernetesingress.namespaces=mcp-platform,mcp-team-alpha" {
		t.Fatalf("traefik ingress watch arg = %q", arg)
	}
	got := platform.intents["team-1"]
	if got.Receipt == nil || got.Receipt.Outcome != apikit.TeamIntentOutcomeApplied || got.Receipt.AppliedRevisionID != "rev-1" {
		t.Fatalf("unexpected receipt: %+v", got.Receipt)
	}
	if len(platform.seenReqIDs) == 0 || platform.seenReqIDs[0] != "req-team-create-1" {
		t.Fatalf("request ID not propagated: %+v", platform.seenReqIDs)
	}
}

func TestWorkerDeleteRemovesTraefikWatchAndRetainsNamespace(t *testing.T) {
	k8s := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mcp-team-alpha",
				UID:  types.UID("uid-alpha"),
				Labels: map[string]string{
					kubeworkload.PlatformManagedLabel: "true",
					k8sprovision.TeamIDLabel:          "team-1",
				},
			},
		},
		makeTraefikDeployment("mcp-platform", "mcp-team-alpha"),
	)
	platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "alpha",
		Namespace:         "mcp-team-alpha",
		DesiredRevisionID: "rev-del",
		Deleted:           true,
		ActiveOwnerTeamID: "",
	})
	w := NewWorker("worker-1", platform, newTestK8sReconciler(k8s))
	if err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-del"}, 1, 8)); err != nil {
		t.Fatalf("Work() delete error = %v", err)
	}
	// Namespace MUST be retained
	if _, err := k8s.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{}); err != nil {
		t.Fatalf("team namespace must be retained on delete, got err: %v", err)
	}
	// Traefik watch MUST be removed
	traefik, _ := k8s.AppsV1().Deployments("traefik").Get(t.Context(), "traefik", metav1.GetOptions{})
	if arg := traefik.Spec.Template.Spec.Containers[0].Args[0]; arg != "--providers.kubernetesingress.namespaces=mcp-platform" {
		t.Fatalf("traefik watch not removed: %q", arg)
	}
	if got := platform.intents["team-1"].Receipt; got == nil || got.Outcome != apikit.TeamIntentOutcomeCleaned || got.AppliedRevisionID != "rev-del" {
		t.Fatalf("unexpected delete receipt: %+v", got)
	}
}

func TestWorkerStaleDeleteAndStaleCreateCannotAffectRecreatedTeam(t *testing.T) {
	t.Run("stale delete job after T2 recreated does not touch Traefik watch or T2 namespace", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mcp-team-alpha",
					UID:  types.UID("uid-t2"),
					Labels: map[string]string{
						kubeworkload.PlatformManagedLabel: "true",
						k8sprovision.TeamIDLabel:          "team-2",
					},
				},
			},
			makeTraefikDeployment("mcp-platform", "mcp-team-alpha"),
		)
		platform := newFakeIntentPlatform(
			apikit.TeamNamespaceIntent{
				TeamID:            "team-1",
				Slug:              "alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-t1-del",
				Deleted:           true,
				ActiveOwnerTeamID: "team-2",
			},
			apikit.TeamNamespaceIntent{
				TeamID:            "team-2",
				Slug:              "alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-t2-create",
				Deleted:           false,
				ActiveOwnerTeamID: "team-2",
			},
		)
		w := NewWorker("worker-stale", platform, newTestK8sReconciler(k8s))
		if err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-t1-del"}, 1, 8)); err != nil {
			t.Fatalf("stale delete Work() error = %v", err)
		}
		// T2's Traefik watch and namespace label MUST remain intact
		traefik, _ := k8s.AppsV1().Deployments("traefik").Get(t.Context(), "traefik", metav1.GetOptions{})
		if arg := traefik.Spec.Template.Spec.Containers[0].Args[0]; arg != "--providers.kubernetesingress.namespaces=mcp-platform,mcp-team-alpha" {
			t.Fatalf("stale T1 delete broke T2 traefik watch: %q", arg)
		}
		ns, _ := k8s.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
		if ns.Labels[k8sprovision.TeamIDLabel] != "team-2" {
			t.Fatalf("T2 namespace label altered: %+v", ns.Labels)
		}
		if rec := platform.intents["team-1"].Receipt; rec == nil || rec.Outcome != apikit.TeamIntentOutcomeSuperseded {
			t.Fatalf("T1 receipt = %+v, want Superseded", rec)
		}
		if platform.intents["team-2"].Receipt != nil {
			t.Fatalf("T2 receipt should not be touched by T1 worker: %+v", platform.intents["team-2"].Receipt)
		}
	})

	t.Run("in-flight stale T1 create racing T2 cannot clobber T2 label or remove T2 watch", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
		platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
			TeamID:            "team-1",
			Slug:              "alpha",
			Namespace:         "mcp-team-alpha",
			DesiredRevisionID: "rev-t1-create",
			Deleted:           false,
			ActiveOwnerTeamID: "team-1",
		})
		w := NewWorker("worker-stale", platform, newTestK8sReconciler(k8s))
		// Simulate T1 being deleted and T2 created & provisioned right before stale T1 mutates K8s!
		w.BeforeMutate = func(ctx context.Context, _ apikit.TeamNamespaceIntent) error {
			platform.mu.Lock()
			t1 := platform.intents["team-1"]
			t1.Deleted = true
			t1.DesiredRevisionID = "rev-t1-del"
			t1.ActiveOwnerTeamID = "team-2"
			platform.intents["team-1"] = t1
			platform.intents["team-2"] = apikit.TeamNamespaceIntent{
				TeamID:            "team-2",
				Slug:              "alpha",
				Namespace:         "mcp-team-alpha",
				DesiredRevisionID: "rev-t2-create",
				ActiveOwnerTeamID: "team-2",
			}
			platform.mu.Unlock()
			// T2 provisions first
			_, err := k8s.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mcp-team-alpha",
					UID:  types.UID("uid-t2"),
					Labels: map[string]string{
						kubeworkload.PlatformManagedLabel: "true",
						k8sprovision.TeamIDLabel:          "team-2",
					},
				},
			}, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			return k8sprovision.EnsureTeamTraefikWatch(ctx, k8s, "mcp-team-alpha", k8sprovision.TraefikWatchConfig{
				Mode: "required", Namespace: "traefik", Deployment: "traefik", ServiceAccount: "traefik",
			})
		}

		if err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-t1-create"}, 1, 8)); err != nil {
			t.Fatalf("racing stale T1 Work() error = %v", err)
		}
		ns, _ := k8s.CoreV1().Namespaces().Get(t.Context(), "mcp-team-alpha", metav1.GetOptions{})
		if ns.Labels[k8sprovision.TeamIDLabel] != "team-2" {
			t.Fatalf("stale T1 clobbered T2 label: %+v", ns.Labels)
		}
		traefik, _ := k8s.AppsV1().Deployments("traefik").Get(t.Context(), "traefik", metav1.GetOptions{})
		if arg := traefik.Spec.Template.Spec.Containers[0].Args[0]; arg != "--providers.kubernetesingress.namespaces=mcp-platform,mcp-team-alpha" {
			t.Fatalf("stale T1 removed T2 traefik watch: %q", arg)
		}
		if rec := platform.intents["team-1"].Receipt; rec != nil && rec.Outcome == apikit.TeamIntentOutcomeApplied {
			t.Fatalf("stale T1 must never record Applied: %+v", rec)
		}
	})
}

func TestWorkerMidProvisioningDeleteRollsBackTraefikWatchAndDoesNotRecordApplied(t *testing.T) {
	k8s := fake.NewSimpleClientset(makePlatformNamespace(), makeTraefikDeployment("mcp-platform"))
	platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
		TeamID:            "team-1",
		Slug:              "epsilon",
		Namespace:         "mcp-team-epsilon",
		DesiredRevisionID: "rev-create",
		ActiveOwnerTeamID: "team-1",
	})
	w := NewWorker("worker-1", platform, newTestK8sReconciler(k8s))
	w.AfterMutate = func(_ context.Context, _ apikit.TeamNamespaceIntent) error {
		platform.mu.Lock()
		defer platform.mu.Unlock()
		it := platform.intents["team-1"]
		it.Deleted = true
		it.DesiredRevisionID = "rev-delete"
		it.ActiveOwnerTeamID = ""
		platform.intents["team-1"] = it
		return nil
	}

	if err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-create"}, 1, 8)); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	traefik, _ := k8s.AppsV1().Deployments("traefik").Get(t.Context(), "traefik", metav1.GetOptions{})
	if arg := traefik.Spec.Template.Spec.Containers[0].Args[0]; arg != "--providers.kubernetesingress.namespaces=mcp-platform" {
		t.Fatalf("traefik watch should have been rolled back on mid-provisioning delete: %q", arg)
	}
	if rec := platform.intents["team-1"].Receipt; rec != nil && rec.Outcome == apikit.TeamIntentOutcomeApplied {
		t.Fatalf("must not record Applied after mid-provisioning delete: %+v", rec)
	}
}

func TestWorkerCancelsTerminalOwnershipConflictAndSurfacesExhaustedRetries(t *testing.T) {
	t.Run("foreign unmanaged namespace is terminal cancelled", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-foreign"}},
			makeTraefikDeployment("mcp-platform"),
		)
		platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
			TeamID:            "team-1",
			Slug:              "foreign",
			Namespace:         "mcp-team-foreign",
			DesiredRevisionID: "rev-1",
			ActiveOwnerTeamID: "team-1",
		})
		w := NewWorker("worker-1", platform, newTestK8sReconciler(k8s))
		err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-1"}, 1, 8))
		var cancelErr *river.JobCancelError
		if !errors.As(err, &cancelErr) {
			t.Fatalf("expected river.JobCancelError, got %T: %v", err, err)
		}
		rec := platform.intents["team-1"].Receipt
		if rec == nil || rec.Outcome != apikit.TeamIntentOutcomeTerminal || rec.LastErrorClass != apikit.TeamIntentErrorOwnershipConflict || rec.AppliedRevisionID != "" {
			t.Fatalf("unexpected terminal receipt: %+v", rec)
		}
	})

	t.Run("final attempt marks exhausted failure without fabricating Applied", func(t *testing.T) {
		k8s := fake.NewSimpleClientset(makeTraefikDeployment("mcp-platform"))
		reconciler := newTestK8sReconciler(k8s)
		reconciler.EnsureRegistryPullSecret = func(context.Context, kubernetes.Interface, string) error {
			return errors.New("k8s api server temporarily unavailable")
		}
		platform := newFakeIntentPlatform(apikit.TeamNamespaceIntent{
			TeamID:            "team-1",
			Slug:              "outage",
			Namespace:         "mcp-team-outage",
			DesiredRevisionID: "rev-1",
			ActiveOwnerTeamID: "team-1",
		})
		w := NewWorker("worker-1", platform, reconciler)
		err := w.Work(t.Context(), newRiverJob(ReconcileJobArgs{TeamID: "team-1", RevisionID: "rev-1"}, 8, 8))
		if err == nil {
			t.Fatal("expected transient error on outage")
		}
		rec := platform.intents["team-1"].Receipt
		if rec == nil || rec.Outcome != apikit.TeamIntentOutcomeFailed || rec.LastErrorClass != apikit.TeamIntentErrorExhausted || rec.AppliedRevisionID != "" {
			t.Fatalf("unexpected exhausted receipt: %+v", rec)
		}
	})
}
