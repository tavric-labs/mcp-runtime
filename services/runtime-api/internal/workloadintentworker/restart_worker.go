package workloadintentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riverqueue/river"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/platformstack"
	"mcp-runtime/pkg/apikit"
)

// RestartPlatformClient defines Platform API calls for RestartWorker.
type RestartPlatformClient interface {
	ClaimRestartOperationFence(ctx context.Context, operationID, workerID string) (apikit.RestartOperationFenceResponse, error)
	RecordRestartComponentCheckpoint(ctx context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error)
	RecordRestartOperationReceipt(ctx context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
}

// RestartPatcher applies an exact fixed restartedAt annotation token to one component.
type RestartPatcher interface {
	PatchComponentRestart(ctx context.Context, component, restartedAt string) (targetUID string, err error)
}

// RestartWorker executes durable imperative component restart operations using
// the fixed acceptance-time RestartedAt token and per-component checkpoints.
type RestartWorker struct {
	river.WorkerDefaults[ReconcileComponentRestartJobArgs]
	platform RestartPlatformClient
	patcher  RestartPatcher
	workerID string
}

// NewRestartWorker constructs a RestartWorker.
func NewRestartWorker(platform RestartPlatformClient, patcher RestartPatcher, workerID string) *RestartWorker {
	return &RestartWorker{platform: platform, patcher: patcher, workerID: workerID}
}

// Work executes one River reconcile_component_restart job.
func (w *RestartWorker) Work(ctx context.Context, job *river.Job[ReconcileComponentRestartJobArgs]) error {
	return w.Reconcile(ctx, job.Args.OperationID, job.Args.DesiredRevisionID, job.Attempt)
}

// Reconcile claims the fence and patches every uncheckpointed target component
// using the fixed RestartedAt token persisted at acceptance.
func (w *RestartWorker) Reconcile(ctx context.Context, operationID, _ string, attempt int) error {
	claim, err := w.platform.ClaimRestartOperationFence(ctx, operationID, w.workerID)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	if strings.TrimSpace(intent.RestartedAt) == "" {
		err := errors.New("restart operation missing fixed restarted_at token")
		_, _ = w.platform.RecordRestartOperationReceipt(ctx, operationID, apikit.RestartOperationReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeTerminal,
			Status:            apikit.RestartStatusFailed,
			Attempt:           attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.WorkloadIntentErrorInvalidConfig,
			LastError:         err.Error(),
		})
		return err
	}

	for _, comp := range intent.TargetComponents {
		if cp, ok := intent.ComponentCheckpoints[comp]; ok && cp.Completed {
			// Already durably restarted under this operation; never repeat on retry.
			continue
		}
		targetUID, err := w.patcher.PatchComponentRestart(ctx, comp, intent.RestartedAt)
		if err != nil {
			_, _ = w.platform.RecordRestartComponentCheckpoint(ctx, operationID, apikit.RestartComponentCheckpointRequest{
				DesiredRevisionID: intent.DesiredRevisionID,
				FenceEpoch:        claim.FenceEpoch,
				WorkerID:          w.workerID,
				Checkpoint: apikit.ComponentRestartCheckpoint{
					Component: comp,
					Completed: false,
					Error:     err.Error(),
				},
			})
			_, _ = w.platform.RecordRestartOperationReceipt(ctx, operationID, apikit.RestartOperationReceiptRequest{
				DesiredRevisionID: intent.DesiredRevisionID,
				FenceEpoch:        claim.FenceEpoch,
				Outcome:           apikit.WorkloadIntentOutcomeFailed,
				Status:            apikit.RestartStatusInProgress,
				Attempt:           attempt,
				WorkerID:          w.workerID,
				LastErrorClass:    apikit.WorkloadIntentErrorTransient,
				LastError:         fmt.Sprintf("restart %s failed: %v", comp, err),
			})
			return err
		}
		now := time.Now().UTC()
		updated, err := w.platform.RecordRestartComponentCheckpoint(ctx, operationID, apikit.RestartComponentCheckpointRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			WorkerID:          w.workerID,
			Checkpoint: apikit.ComponentRestartCheckpoint{
				Component: comp,
				Completed: true,
				PatchedAt: &now,
				TargetUID: targetUID,
			},
		})
		if err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
		intent = updated
	}

	_, err = w.platform.RecordRestartOperationReceipt(ctx, operationID, apikit.RestartOperationReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        claim.FenceEpoch,
		Outcome:           apikit.WorkloadIntentOutcomeApplied,
		Status:            apikit.RestartStatusCompleted,
		Attempt:           attempt,
		WorkerID:          w.workerID,
	})
	if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
		return nil
	}
	return err
}

// KubeRestartPatcher applies fixed restartedAt annotations to platform stack workloads.
type KubeRestartPatcher struct {
	clientset kubernetes.Interface
}

// NewKubeRestartPatcher creates a Kubernetes-backed RestartPatcher.
func NewKubeRestartPatcher(clientset kubernetes.Interface) *KubeRestartPatcher {
	return &KubeRestartPatcher{clientset: clientset}
}

func (p *KubeRestartPatcher) PatchComponentRestart(ctx context.Context, key, restartedAt string) (string, error) {
	component, err := platformstack.FindComponent(key)
	if err != nil {
		return "", err
	}
	patchBytes, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": restartedAt,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal restart patch: %w", err)
	}

	switch component.Kind {
	case "deployment":
		dep, err := p.clientset.AppsV1().Deployments(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return "", fmt.Errorf("patch deployment %s: %w", component.Key, err)
		}
		return string(dep.UID), nil
	case "statefulset":
		sts, err := p.clientset.AppsV1().StatefulSets(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return "", fmt.Errorf("patch statefulset %s: %w", component.Key, err)
		}
		return string(sts.UID), nil
	case "daemonset":
		ds, err := p.clientset.AppsV1().DaemonSets(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return "", fmt.Errorf("patch daemonset %s: %w", component.Key, err)
		}
		return string(ds.UID), nil
	default:
		return "", fmt.Errorf("unsupported component kind %q for restart", component.Kind)
	}
}
