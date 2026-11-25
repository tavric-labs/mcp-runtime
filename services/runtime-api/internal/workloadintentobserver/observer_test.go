package workloadintentobserver

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"mcp-runtime-api/internal/platformapi"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/apikit"
)

type fakeObserverPlatform struct {
	leaseEpoch         int64
	servers            []apikit.ServerIntent
	deployments        []apikit.DeploymentIntent
	serverObsCalls     []apikit.ServerIntentObservationRequest
	deploymentObsCalls []apikit.DeploymentIntentObservationRequest
	failNextServer409  bool
	enqueuedApplyJobs  int
}

func (f *fakeObserverPlatform) ClaimWorkloadObserverLease(_ context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error) {
	f.leaseEpoch++
	if clusterKey == "" {
		clusterKey = "default"
	}
	return apikit.WorkloadObserverLeaseResponse{ClusterKey: clusterKey, ObserverID: observerID, ObserverEpoch: f.leaseEpoch}, nil
}

func (f *fakeObserverPlatform) ListServerIntents(context.Context) ([]apikit.ServerIntent, error) {
	return append([]apikit.ServerIntent(nil), f.servers...), nil
}

func (f *fakeObserverPlatform) RecordServerIntentObservation(_ context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if f.failNextServer409 {
		f.failNextServer409 = false
		return apikit.WorkloadStatusObservation{}, platformapi.ErrWorkloadIntentConflict
	}
	f.serverObsCalls = append(f.serverObsCalls, req)
	obs := apikit.WorkloadStatusObservation{
		ObserverID:         req.ObserverID,
		ObserverEpoch:      req.ObserverEpoch,
		ObservedRevisionID: req.ObservedRevisionID,
		TargetUID:          req.TargetUID,
		ObservedGeneration: req.ObservedGeneration,
		Health:             req.Health,
		Reason:             req.Reason,
		Message:            req.Message,
		Conditions:         req.Conditions,
	}
	for i := range f.servers {
		if f.servers[i].ServerID == serverID {
			f.servers[i].Observation = &obs
		}
	}
	return obs, nil
}

func (f *fakeObserverPlatform) ListDeploymentIntents(context.Context) ([]apikit.DeploymentIntent, error) {
	return append([]apikit.DeploymentIntent(nil), f.deployments...), nil
}

func (f *fakeObserverPlatform) RecordDeploymentIntentObservation(_ context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	f.deploymentObsCalls = append(f.deploymentObsCalls, req)
	obs := apikit.WorkloadStatusObservation{
		ObserverID:         req.ObserverID,
		ObserverEpoch:      req.ObserverEpoch,
		ObservedRevisionID: req.ObservedRevisionID,
		TargetUID:          req.TargetUID,
		ObservedGeneration: req.ObservedGeneration,
		Health:             req.Health,
		Reason:             req.Reason,
		Message:            req.Message,
	}
	for i := range f.deployments {
		if f.deployments[i].DeploymentID == deploymentID {
			f.deployments[i].Observation = &obs
		}
	}
	return obs, nil
}

type fakeClusterReader struct {
	servers     map[string]*mcpv1alpha1.MCPServer
	deployments map[string]*appsv1.Deployment
}

func (r *fakeClusterReader) GetMCPServer(_ context.Context, namespace, name string) (*mcpv1alpha1.MCPServer, bool, error) {
	srv, ok := r.servers[namespace+"/"+name]
	return srv, ok, nil
}

func (r *fakeClusterReader) GetDeployment(_ context.Context, namespace, name string) (*appsv1.Deployment, bool, error) {
	dep, ok := r.deployments[namespace+"/"+name]
	return dep, ok, nil
}

func TestServerObserverProgressingReadyThenDegradedWithoutReenqueuingApply(t *testing.T) {
	ctx := context.Background()
	plat := &fakeObserverPlatform{
		servers: []apikit.ServerIntent{{
			ServerID:          "srv-1",
			Namespace:         "mcp-team-alpha",
			Name:              "billing",
			DesiredRevisionID: "rev-1",
			Lifecycle:         apikit.ServerLifecycleActive,
			Receipt: &apikit.WorkloadExecutionReceipt{
				FenceEpoch:        1,
				AppliedRevisionID: "rev-1",
				Outcome:           apikit.WorkloadIntentOutcomeApplied,
				TargetUID:         "uid-cr-1",
				TargetGeneration:  2,
			},
		}},
	}
	cluster := &fakeClusterReader{
		servers: map[string]*mcpv1alpha1.MCPServer{
			"mcp-team-alpha/billing": {
				ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: "mcp-team-alpha", UID: types.UID("uid-cr-1"), Generation: 2},
				Status: mcpv1alpha1.MCPServerStatus{
					Conditions: []metav1.Condition{
						{Type: ConditionDeploymentReady, Status: metav1.ConditionFalse, Reason: "Progressing", ObservedGeneration: 2},
						{Type: ConditionServiceReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
					},
				},
			},
		},
	}

	obs := New(plat, cluster, Config{ClusterKey: "default", ObserverID: "obs-1"})

	// Phase 1: Apply completed, rollout still in progress -> Progressing.
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	if got := plat.servers[0].Observation.Health; got != apikit.WorkloadIntentHealthProgressing {
		t.Fatalf("phase 1 health = %q, want Progressing", got)
	}

	// Phase 2: Operator reports all conditions Ready at generation 2 -> Ready.
	cluster.servers["mcp-team-alpha/billing"].Status = mcpv1alpha1.MCPServerStatus{
		DeploymentReady: true,
		ServiceReady:    true,
		IngressReady:    true,
		GatewayReady:    true,
		PolicyReady:     true,
		Conditions: []metav1.Condition{
			{Type: ConditionDeploymentReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
			{Type: ConditionServiceReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
			{Type: ConditionIngressReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
			{Type: ConditionGatewayReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
			{Type: ConditionPolicyReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2},
		},
	}
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if got := plat.servers[0].Observation.Health; got != apikit.WorkloadIntentHealthReady {
		t.Fatalf("phase 2 health = %q, want Ready", got)
	}

	// Phase 3: Simulated node/pod failure flips DeploymentReady to False -> ONLY health becomes Degraded,
	// receipt stays untouched, and NO apply job is enqueued!
	cluster.servers["mcp-team-alpha/billing"].Status.DeploymentReady = false
	cluster.servers["mcp-team-alpha/billing"].Status.Conditions[0].Status = metav1.ConditionFalse
	cluster.servers["mcp-team-alpha/billing"].Status.Conditions[0].Reason = "CrashLoopBackOff"

	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce #3: %v", err)
	}
	if got := plat.servers[0].Observation.Health; got != apikit.WorkloadIntentHealthDegraded {
		t.Fatalf("phase 3 health = %q, want Degraded", got)
	}
	if plat.servers[0].Receipt.Outcome != apikit.WorkloadIntentOutcomeApplied {
		t.Fatalf("completed receipt should stay untouched, got %+v", plat.servers[0].Receipt)
	}
	if plat.enqueuedApplyJobs != 0 {
		t.Fatalf("observer must never enqueue apply jobs, got %d", plat.enqueuedApplyJobs)
	}
}

func TestServerObserverUIDMismatchIsDegradedNeverReady(t *testing.T) {
	ctx := context.Background()
	plat := &fakeObserverPlatform{
		servers: []apikit.ServerIntent{{
			ServerID:          "srv-uid",
			Namespace:         "mcp-team-alpha",
			Name:              "billing",
			DesiredRevisionID: "rev-1",
			Lifecycle:         apikit.ServerLifecycleActive,
			Receipt: &apikit.WorkloadExecutionReceipt{
				AppliedRevisionID: "rev-1",
				Outcome:           apikit.WorkloadIntentOutcomeApplied,
				TargetUID:         "uid-expected",
				TargetGeneration:  1,
			},
		}},
	}
	cluster := &fakeClusterReader{
		servers: map[string]*mcpv1alpha1.MCPServer{
			"mcp-team-alpha/billing": {
				ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: "mcp-team-alpha", UID: types.UID("uid-different"), Generation: 1},
				Status: mcpv1alpha1.MCPServerStatus{
					Conditions: []metav1.Condition{
						{Type: ConditionDeploymentReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1},
					},
				},
			},
		},
	}

	obs := New(plat, cluster, Config{ObserverID: "obs-1"})
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := plat.servers[0].Observation.Health; got != apikit.WorkloadIntentHealthDegraded {
		t.Fatalf("UID mismatch health = %q, want Degraded", got)
	}
	if plat.servers[0].Observation.Reason != "UIDMismatch" {
		t.Fatalf("UID mismatch reason = %q, want UIDMismatch", plat.servers[0].Observation.Reason)
	}
}

func TestObserverRefreshesLeaseEpochAfter409Conflict(t *testing.T) {
	ctx := context.Background()
	plat := &fakeObserverPlatform{
		failNextServer409: true,
		servers: []apikit.ServerIntent{{
			ServerID:          "srv-1",
			Namespace:         "mcp-team-alpha",
			Name:              "billing",
			DesiredRevisionID: "rev-1",
			Lifecycle:         apikit.ServerLifecycleActive,
		}},
	}
	cluster := &fakeClusterReader{servers: map[string]*mcpv1alpha1.MCPServer{}}
	obs := New(plat, cluster, Config{ObserverID: "obs-1"})

	// Cycle 1 gets 409 conflict and invalidates local epoch without failing fatally.
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	if plat.leaseEpoch != 1 {
		t.Fatalf("leaseEpoch after #1 = %d, want 1", plat.leaseEpoch)
	}

	// Cycle 2 re-claims lease epoch (2) and records observation cleanly.
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if plat.leaseEpoch != 2 {
		t.Fatalf("leaseEpoch after #2 = %d, want 2", plat.leaseEpoch)
	}
	if len(plat.serverObsCalls) != 1 || plat.serverObsCalls[0].ObserverEpoch != 2 {
		t.Fatalf("expected observation recorded at epoch 2, got %+v", plat.serverObsCalls)
	}
}

func TestDeploymentObserverMapsReadinessAndRuntimeFailure(t *testing.T) {
	ctx := context.Background()
	replicas := int32(2)
	plat := &fakeObserverPlatform{
		deployments: []apikit.DeploymentIntent{{
			DeploymentID:      "dep-1",
			Namespace:         "mcp-team-alpha",
			Name:              "raw-web",
			DesiredRevisionID: "rev-dep-1",
			Replicas:          2,
			Receipt: &apikit.WorkloadExecutionReceipt{
				AppliedRevisionID: "rev-dep-1",
				Outcome:           apikit.WorkloadIntentOutcomeApplied,
				TargetUID:         "uid-dep-1",
				TargetGeneration:  3,
			},
		}},
	}
	cluster := &fakeClusterReader{
		deployments: map[string]*appsv1.Deployment{
			"mcp-team-alpha/raw-web": {
				ObjectMeta: metav1.ObjectMeta{Name: "raw-web", Namespace: "mcp-team-alpha", UID: types.UID("uid-dep-1"), Generation: 3},
				Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 3,
					UpdatedReplicas:    2,
					ReadyReplicas:      2,
					AvailableReplicas:  2,
					Conditions: []appsv1.DeploymentCondition{
						{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
					},
				},
			},
		},
	}

	obs := New(plat, cluster, Config{ObserverID: "obs-dep"})
	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce Ready: %v", err)
	}
	if got := plat.deployments[0].Observation.Health; got != apikit.WorkloadIntentHealthReady {
		t.Fatalf("deployment health = %q, want Ready", got)
	}

	// Simulate pod crash -> Degraded without touching receipt.
	cluster.deployments["mcp-team-alpha/raw-web"].Status.ReadyReplicas = 0
	cluster.deployments["mcp-team-alpha/raw-web"].Status.AvailableReplicas = 0
	cluster.deployments["mcp-team-alpha/raw-web"].Status.Conditions[0].Status = corev1.ConditionFalse
	cluster.deployments["mcp-team-alpha/raw-web"].Status.Conditions[0].Reason = "MinimumReplicasUnavailable"

	if err := obs.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce Degraded: %v", err)
	}
	if got := plat.deployments[0].Observation.Health; got != apikit.WorkloadIntentHealthDegraded {
		t.Fatalf("deployment health after crash = %q, want Degraded", got)
	}
}
