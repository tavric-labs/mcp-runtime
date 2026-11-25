package workloadintentworker

import (
	"context"
	"errors"
	"strings"

	"github.com/riverqueue/river"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/kubeworkload"
)

// DeploymentPlatformClient defines the Platform API operations for DeploymentWorker.
type DeploymentPlatformClient interface {
	ClaimDeploymentIntentFence(ctx context.Context, deploymentID, workerID string) (apikit.DeploymentIntentFenceResponse, error)
	RecordDeploymentIntentReceipt(ctx context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
}

// DeploymentExecutor applies or UID-conditionally deletes raw Kubernetes
// Deployments and Services directly without passing through MCPServer operator.
type DeploymentExecutor interface {
	ApplyDeploymentAndService(ctx context.Context, intent apikit.DeploymentIntent) (ApplyResult, error)
	DeleteDeploymentAndServiceByUID(ctx context.Context, namespace, name, targetUID string) error
}

// DeploymentWorker reconciles raw managed Deployment/Service intents.
type DeploymentWorker struct {
	river.WorkerDefaults[ReconcileDeploymentJobArgs]
	platform DeploymentPlatformClient
	exec     DeploymentExecutor
	workerID string
}

// NewDeploymentWorker constructs a DeploymentWorker.
func NewDeploymentWorker(platform DeploymentPlatformClient, exec DeploymentExecutor, workerID string) *DeploymentWorker {
	return &DeploymentWorker{platform: platform, exec: exec, workerID: workerID}
}

// Work executes one River reconcile_deployment job.
func (w *DeploymentWorker) Work(ctx context.Context, job *river.Job[ReconcileDeploymentJobArgs]) error {
	return w.Reconcile(ctx, job.Args.DeploymentID, job.Args.DesiredRevisionID, job.Attempt)
}

// Reconcile claims the fence and applies or UID-conditionally deletes the raw Deployment/Service.
func (w *DeploymentWorker) Reconcile(ctx context.Context, deploymentID, jobRevisionID string, attempt int) error {
	claim, err := w.platform.ClaimDeploymentIntentFence(ctx, deploymentID, w.workerID)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	if jobRevisionID != "" && intent.DesiredRevisionID != jobRevisionID {
		_, _ = w.platform.RecordDeploymentIntentReceipt(ctx, deploymentID, apikit.DeploymentIntentReceiptRequest{
			DesiredRevisionID: jobRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeSuperseded,
			Attempt:           attempt,
			WorkerID:          w.workerID,
		})
		return nil
	}

	if intent.Deleted {
		targetUID := intent.TargetDeploymentUID
		if targetUID == "" && intent.Receipt != nil {
			targetUID = intent.Receipt.TargetUID
		}
		if err := w.exec.DeleteDeploymentAndServiceByUID(ctx, intent.Namespace, intent.Name, targetUID); err != nil {
			_, _ = w.platform.RecordDeploymentIntentReceipt(ctx, deploymentID, apikit.DeploymentIntentReceiptRequest{
				DesiredRevisionID: intent.DesiredRevisionID,
				FenceEpoch:        claim.FenceEpoch,
				Outcome:           apikit.WorkloadIntentOutcomeFailed,
				TargetUID:         targetUID,
				Attempt:           attempt,
				WorkerID:          w.workerID,
				LastErrorClass:    apikit.WorkloadIntentErrorTransient,
				LastError:         err.Error(),
			})
			return err
		}
		_, err = w.platform.RecordDeploymentIntentReceipt(ctx, deploymentID, apikit.DeploymentIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeCleaned,
			TargetUID:         targetUID,
			Attempt:           attempt,
			WorkerID:          w.workerID,
		})
		if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
			return nil
		}
		return err
	}

	applied, err := w.exec.ApplyDeploymentAndService(ctx, intent)
	if err != nil {
		_, _ = w.platform.RecordDeploymentIntentReceipt(ctx, deploymentID, apikit.DeploymentIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeFailed,
			Attempt:           attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.WorkloadIntentErrorTransient,
			LastError:         err.Error(),
		})
		return err
	}
	_, err = w.platform.RecordDeploymentIntentReceipt(ctx, deploymentID, apikit.DeploymentIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        claim.FenceEpoch,
		Outcome:           apikit.WorkloadIntentOutcomeApplied,
		TargetUID:         applied.UID,
		TargetGeneration:  applied.Generation,
		SpecDigest:        intent.SpecDigest,
		Attempt:           attempt,
		WorkerID:          w.workerID,
	})
	if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
		return nil
	}
	return err
}

// KubeDeploymentExecutor manages raw Kubernetes Deployments and Services.
type KubeDeploymentExecutor struct {
	clientset kubernetes.Interface
}

// NewKubeDeploymentExecutor creates a Kubernetes-backed DeploymentExecutor.
func NewKubeDeploymentExecutor(clientset kubernetes.Interface) *KubeDeploymentExecutor {
	return &KubeDeploymentExecutor{clientset: clientset}
}

func (e *KubeDeploymentExecutor) ApplyDeploymentAndService(ctx context.Context, intent apikit.DeploymentIntent) (ApplyResult, error) {
	port := intent.Port
	if port <= 0 {
		port = 8088
	}
	replicas := intent.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	image := strings.TrimSpace(intent.Image)
	if intent.Version != "" && !strings.Contains(image[strings.LastIndex(image, "/")+1:], ":") {
		image += ":" + strings.TrimSpace(intent.Version)
	}
	labels := map[string]string{
		"app.kubernetes.io/name":          intent.Name,
		"app.kubernetes.io/managed-by":    "mcp-runtime",
		kubeworkload.PlatformManagedLabel: "true",
	}
	if intent.TeamID != "" {
		labels["mcpruntime.org/team-id"] = intent.TeamID
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.Name,
			Namespace: intent.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"mcpruntime.org/desired-revision-id": intent.DesiredRevisionID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": intent.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "server",
						Image:           image,
						Ports:           []corev1.ContainerPort{{ContainerPort: port}},
						SecurityContext: kubeworkload.RestrictedContainerSecurityContext(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
	}
	kubeworkload.ApplyRestrictedPodDefaults(&dep.Spec.Template.Spec)

	deps := e.clientset.AppsV1().Deployments(intent.Namespace)
	var applied *appsv1.Deployment
	existing, err := deps.Get(ctx, intent.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		applied, err = deps.Create(ctx, dep, metav1.CreateOptions{})
	} else if err == nil {
		existing.Labels = dep.Labels
		existing.Annotations = dep.Annotations
		existing.Spec = dep.Spec
		applied, err = deps.Update(ctx, existing, metav1.UpdateOptions{})
	}
	if err != nil {
		return ApplyResult{}, err
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.Name,
			Namespace: intent.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": intent.Name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(int(port))}},
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
	svcs := e.clientset.CoreV1().Services(intent.Namespace)
	existingSvc, err := svcs.Get(ctx, intent.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = svcs.Create(ctx, svc, metav1.CreateOptions{})
	} else if err == nil {
		existingSvc.Labels = svc.Labels
		existingSvc.Spec.Ports = svc.Spec.Ports
		existingSvc.Spec.Selector = svc.Spec.Selector
		_, err = svcs.Update(ctx, existingSvc, metav1.UpdateOptions{})
	}
	if err != nil {
		return ApplyResult{}, err
	}

	return ApplyResult{UID: string(applied.UID), Generation: applied.Generation}, nil
}

func (e *KubeDeploymentExecutor) DeleteDeploymentAndServiceByUID(ctx context.Context, namespace, name, targetUID string) error {
	deps := e.clientset.AppsV1().Deployments(namespace)
	existing, err := deps.Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if targetUID != "" && string(existing.UID) != targetUID {
			// A replacement deployment with a new UID exists; do not delete it or its Service.
			return nil
		}
		var opts metav1.DeleteOptions
		if targetUID != "" {
			uid := types.UID(targetUID)
			opts.Preconditions = &metav1.Preconditions{UID: &uid}
		}
		if delErr := deps.Delete(ctx, name, opts); delErr != nil && !apierrors.IsNotFound(delErr) && !apierrors.IsConflict(delErr) {
			return delErr
		}
	}
	if delErr := e.clientset.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
		return delErr
	}
	return nil
}
