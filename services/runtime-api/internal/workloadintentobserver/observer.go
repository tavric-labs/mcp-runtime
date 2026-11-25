// Package workloadintentobserver independently observes Kubernetes MCPServer
// CR conditions and raw Deployment readiness, reporting monotonic
// observer_epoch health transitions back to Platform API without mutating
// desired intent, execution receipts, or River jobs.
package workloadintentobserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/platformapi"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/controlplane"
)

// Operator condition types matching internal/operator/conditions.go.
const (
	ConditionDeploymentReady = "DeploymentReady"
	ConditionServiceReady    = "ServiceReady"
	ConditionIngressReady    = "IngressReady"
	ConditionGatewayReady    = "GatewayReady"
	ConditionPolicyReady     = "PolicyReady"
	ConditionCanaryReady     = "CanaryReady"
)

// PlatformClient defines the Platform API operations needed by Observer.
type PlatformClient interface {
	ClaimWorkloadObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error)
	ListServerIntents(ctx context.Context) ([]apikit.ServerIntent, error)
	RecordServerIntentObservation(ctx context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error)
	ListDeploymentIntents(ctx context.Context) ([]apikit.DeploymentIntent, error)
	RecordDeploymentIntentObservation(ctx context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error)
}

// ClusterReader reads live Kubernetes MCPServer CRs and raw Deployments.
type ClusterReader interface {
	GetMCPServer(ctx context.Context, namespace, name string) (*mcpv1alpha1.MCPServer, bool, error)
	GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, bool, error)
}

// Config configures the independent status observer loop.
type Config struct {
	ClusterKey   string
	ObserverID   string
	PollInterval time.Duration
}

// Observer evaluates live Kubernetes state for ServerIntents and
// DeploymentIntents and records health transitions fenced by observer_epoch.
type Observer struct {
	platform PlatformClient
	cluster  ClusterReader
	cfg      Config

	mu    sync.Mutex
	epoch int64
}

// New creates an Observer.
func New(platform PlatformClient, cluster ClusterReader, cfg Config) *Observer {
	if strings.TrimSpace(cfg.ClusterKey) == "" {
		cfg.ClusterKey = "default"
	}
	if strings.TrimSpace(cfg.ObserverID) == "" {
		cfg.ObserverID = "runtime-workload-observer"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Second
	}
	return &Observer{platform: platform, cluster: cluster, cfg: cfg}
}

// Run polls and reports workload observations until ctx is canceled.
func (o *Observer) Run(ctx context.Context) {
	ticker := time.NewTicker(o.cfg.PollInterval)
	defer ticker.Stop()
	_ = o.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = o.RunOnce(ctx)
		}
	}
}

// RunOnce performs one complete observation pass across servers and deployments.
func (o *Observer) RunOnce(ctx context.Context) error {
	epoch, err := o.ensureLease(ctx)
	if err != nil {
		return err
	}

	servers, err := o.platform.ListServerIntents(ctx)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentDisabled) {
			return nil
		}
		return err
	}
	for _, intent := range servers {
		req := o.evaluateServer(ctx, intent, epoch)
		if _, err := o.platform.RecordServerIntentObservation(ctx, intent.ServerID, req); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				// Stale observer epoch or superseded revision: reset local epoch so next cycle re-claims lease.
				o.invalidateLease(epoch)
				return nil
			}
			return err
		}
	}

	deployments, err := o.platform.ListDeploymentIntents(ctx)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentDisabled) {
			return nil
		}
		return err
	}
	for _, intent := range deployments {
		req := o.evaluateDeployment(ctx, intent, epoch)
		if _, err := o.platform.RecordDeploymentIntentObservation(ctx, intent.DeploymentID, req); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				o.invalidateLease(epoch)
				return nil
			}
			return err
		}
	}
	return nil
}

func (o *Observer) ensureLease(ctx context.Context) (int64, error) {
	o.mu.Lock()
	epoch := o.epoch
	o.mu.Unlock()
	if epoch > 0 {
		return epoch, nil
	}
	lease, err := o.platform.ClaimWorkloadObserverLease(ctx, o.cfg.ClusterKey, o.cfg.ObserverID)
	if err != nil {
		return 0, err
	}
	o.mu.Lock()
	o.epoch = lease.ObserverEpoch
	o.mu.Unlock()
	return lease.ObserverEpoch, nil
}

func (o *Observer) invalidateLease(staleEpoch int64) {
	o.mu.Lock()
	if o.epoch == staleEpoch {
		o.epoch = 0
	}
	o.mu.Unlock()
}

func (o *Observer) evaluateServer(ctx context.Context, intent apikit.ServerIntent, epoch int64) apikit.ServerIntentObservationRequest {
	now := time.Now().UTC()
	req := apikit.ServerIntentObservationRequest{
		ClusterKey:    o.cfg.ClusterKey,
		ObserverID:    o.cfg.ObserverID,
		ObserverEpoch: epoch,
		VerifiedAt:    now,
	}
	if intent.Receipt != nil && intent.Receipt.AppliedRevisionID != "" {
		req.ObservedRevisionID = intent.Receipt.AppliedRevisionID
	}

	srv, found, err := o.cluster.GetMCPServer(ctx, intent.Namespace, intent.Name)
	if err != nil {
		req.Health = apikit.WorkloadIntentHealthUnknown
		req.Reason = "ClusterReadError"
		req.Message = err.Error()
		return req
	}

	switch intent.Lifecycle {
	case apikit.ServerLifecycleRetired:
		if !found {
			req.Health = apikit.WorkloadIntentHealthDeleted
			req.Reason = "Retired"
			return req
		}
		req.TargetUID = string(srv.UID)
		req.ObservedGeneration = srv.Generation
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "Retiring"
		return req
	case apikit.ServerLifecycleRetiring:
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "Retiring"
		if found {
			req.TargetUID = string(srv.UID)
			req.ObservedGeneration = srv.Generation
		}
		return req
	}

	if intent.Receipt == nil || intent.Receipt.AppliedRevisionID == "" || intent.Receipt.AppliedRevisionID != intent.DesiredRevisionID {
		req.Health = apikit.WorkloadIntentHealthPending
		req.Reason = "AwaitingApply"
		return req
	}
	if !found {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = "ServerMissing"
		req.Message = "applied MCPServer CR is missing from cluster"
		return req
	}

	req.TargetUID = string(srv.UID)
	req.ObservedGeneration = srv.Generation

	// UID mismatch MUST report Degraded and NEVER Ready.
	if intent.Receipt.TargetUID != "" && string(srv.UID) != intent.Receipt.TargetUID {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = "UIDMismatch"
		req.Message = fmt.Sprintf("live MCPServer UID %s does not match applied UID %s", srv.UID, intent.Receipt.TargetUID)
		return req
	}

	targetGen := intent.Receipt.TargetGeneration
	if targetGen <= 0 {
		targetGen = srv.Generation
	}

	// Map actual MCPServer operator conditions truthfully.
	conds := make([]apikit.WorkloadIntentCondition, 0, len(srv.Status.Conditions))
	allTrue := true
	sawDeploymentReady := false
	generationLagging := false
	degradedReason := ""
	degradedMsg := ""
	previouslyReady := intent.Observation != nil && intent.Observation.Health == apikit.WorkloadIntentHealthReady

	for _, c := range srv.Status.Conditions {
		conds = append(conds, apikit.WorkloadIntentCondition{
			Type:             c.Type,
			Status:           string(c.Status),
			Reason:           c.Reason,
			Message:          c.Message,
			RevisionID:       req.ObservedRevisionID,
			LastTransitionAt: c.LastTransitionTime.Time.UTC(),
		})
		if c.ObservedGeneration > 0 && c.ObservedGeneration < targetGen {
			generationLagging = true
		}
		if c.Type == ConditionDeploymentReady && c.Status == metav1.ConditionTrue {
			sawDeploymentReady = true
		}
		if c.Status != metav1.ConditionTrue {
			allTrue = false
			if isDegradedConditionReason(c.Reason) || previouslyReady {
				if degradedReason == "" {
					degradedReason = c.Reason
					if degradedReason == "" {
						degradedReason = c.Type + "False"
					}
					degradedMsg = c.Message
				}
			}
		}
	}
	req.Conditions = conds

	if len(srv.Status.Conditions) == 0 {
		if srv.Status.DeploymentReady && srv.Status.ServiceReady {
			req.Health = apikit.WorkloadIntentHealthReady
			req.Reason = "Ready"
			return req
		}
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "AwaitingOperatorConditions"
		return req
	}

	if generationLagging {
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "GenerationPending"
		return req
	}
	if allTrue && sawDeploymentReady {
		req.Health = apikit.WorkloadIntentHealthReady
		req.Reason = "Ready"
		return req
	}
	if degradedReason != "" {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = degradedReason
		req.Message = degradedMsg
		return req
	}
	req.Health = apikit.WorkloadIntentHealthProgressing
	req.Reason = "RolloutInProgress"
	return req
}

func (o *Observer) evaluateDeployment(ctx context.Context, intent apikit.DeploymentIntent, epoch int64) apikit.DeploymentIntentObservationRequest {
	now := time.Now().UTC()
	req := apikit.DeploymentIntentObservationRequest{
		ClusterKey:    o.cfg.ClusterKey,
		ObserverID:    o.cfg.ObserverID,
		ObserverEpoch: epoch,
		VerifiedAt:    now,
	}
	if intent.Receipt != nil && intent.Receipt.AppliedRevisionID != "" {
		req.ObservedRevisionID = intent.Receipt.AppliedRevisionID
	}

	dep, found, err := o.cluster.GetDeployment(ctx, intent.Namespace, intent.Name)
	if err != nil {
		req.Health = apikit.WorkloadIntentHealthUnknown
		req.Reason = "ClusterReadError"
		req.Message = err.Error()
		return req
	}
	if intent.Deleted {
		if !found {
			req.Health = apikit.WorkloadIntentHealthDeleted
			req.Reason = "Deleted"
			return req
		}
		req.TargetUID = string(dep.UID)
		req.ObservedGeneration = dep.Status.ObservedGeneration
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "Deleting"
		return req
	}
	if intent.Receipt == nil || intent.Receipt.AppliedRevisionID == "" || intent.Receipt.AppliedRevisionID != intent.DesiredRevisionID {
		req.Health = apikit.WorkloadIntentHealthPending
		req.Reason = "AwaitingApply"
		return req
	}
	if !found {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = "DeploymentMissing"
		req.Message = "applied Deployment is missing from cluster"
		return req
	}

	req.TargetUID = string(dep.UID)
	req.ObservedGeneration = dep.Status.ObservedGeneration

	if intent.Receipt.TargetUID != "" && string(dep.UID) != intent.Receipt.TargetUID {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = "UIDMismatch"
		req.Message = fmt.Sprintf("live Deployment UID %s does not match applied UID %s", dep.UID, intent.Receipt.TargetUID)
		return req
	}

	targetGen := intent.Receipt.TargetGeneration
	if targetGen <= 0 {
		targetGen = dep.Generation
	}
	if dep.Status.ObservedGeneration < targetGen {
		req.Health = apikit.WorkloadIntentHealthProgressing
		req.Reason = "GenerationPending"
		return req
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	availableTrue := false
	availableFalse := false
	progressingFalse := false
	conds := make([]apikit.WorkloadIntentCondition, 0, len(dep.Status.Conditions))
	for _, c := range dep.Status.Conditions {
		conds = append(conds, apikit.WorkloadIntentCondition{
			Type:             string(c.Type),
			Status:           string(c.Status),
			Reason:           c.Reason,
			Message:          c.Message,
			RevisionID:       req.ObservedRevisionID,
			LastTransitionAt: c.LastTransitionTime.Time.UTC(),
		})
		if c.Type == appsv1.DeploymentAvailable {
			if c.Status == corev1.ConditionTrue {
				availableTrue = true
			} else if c.Status == corev1.ConditionFalse {
				availableFalse = true
			}
		}
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			progressingFalse = true
		}
	}
	req.Conditions = conds

	if dep.Status.ReadyReplicas >= desired && dep.Status.AvailableReplicas >= desired && (availableTrue || desired == 0) {
		req.Health = apikit.WorkloadIntentHealthReady
		req.Reason = "Ready"
		return req
	}
	previouslyReady := intent.Observation != nil && intent.Observation.Health == apikit.WorkloadIntentHealthReady
	if progressingFalse || (previouslyReady && (availableFalse || dep.Status.ReadyReplicas < desired)) {
		req.Health = apikit.WorkloadIntentHealthDegraded
		req.Reason = "MinimumReplicasUnavailable"
		return req
	}
	req.Health = apikit.WorkloadIntentHealthProgressing
	req.Reason = "RolloutInProgress"
	return req
}

func isDegradedConditionReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "Progressing", "Initializing", "Pending", "Reconciling", "RolloutInProgress":
		return false
	default:
		return true
	}
}

// KubeClusterReader implements ClusterReader against Kubernetes dynamic and core clients.
type KubeClusterReader struct {
	dynamic   dynamic.Interface
	clientset kubernetes.Interface
}

// NewKubeClusterReader constructs a Kubernetes-backed ClusterReader.
func NewKubeClusterReader(dyn dynamic.Interface, cs kubernetes.Interface) *KubeClusterReader {
	return &KubeClusterReader{dynamic: dyn, clientset: cs}
}

func (r *KubeClusterReader) GetMCPServer(ctx context.Context, namespace, name string) (*mcpv1alpha1.MCPServer, bool, error) {
	u, err := r.dynamic.Resource(controlplane.MCPServerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var srv mcpv1alpha1.MCPServer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &srv); err != nil {
		return nil, false, err
	}
	return &srv, true, nil
}

func (r *KubeClusterReader) GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, bool, error) {
	dep, err := r.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return dep, true, nil
}
