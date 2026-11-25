package workloadintentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riverqueue/river"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/k8sstore"
	"mcp-runtime-api/internal/platformapi"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/sessionstate"
)

// ServerPlatformClient defines the Platform API operations used by ServerWorker.
type ServerPlatformClient interface {
	ClaimServerIntentFence(ctx context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error)
	RecordServerRetirementCheckpoint(ctx context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error)
	RecordServerIntentReceipt(ctx context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
	GetImagePublicationIntent(ctx context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error)
}

// ApplyResult captures the Kubernetes identity returned after applying a workload.
type ApplyResult struct {
	UID        string
	Generation int64
}

// ServerExecutor performs bounded Kubernetes apply and ordered retirement steps.
type ServerExecutor interface {
	ApplyMCPServer(ctx context.Context, intent apikit.ServerIntent) (ApplyResult, error)
	MarkRetiring(ctx context.Context, namespace, name, targetCRUID string) (string, error)
	FenceSessions(ctx context.Context, namespace, name, targetCRUID string) error
	RetireGrants(ctx context.Context, namespace, name string) error
	CleanCredentials(ctx context.Context, namespace, name string) error
	DeleteMCPServerByUID(ctx context.Context, namespace, name, targetCRUID string) error
}

// ServerWorker reconciles MCPServer intents and ordered retirements.
type ServerWorker struct {
	river.WorkerDefaults[ReconcileServerJobArgs]
	platform ServerPlatformClient
	exec     ServerExecutor
	workerID string
}

// NewServerWorker constructs a ServerWorker.
func NewServerWorker(platform ServerPlatformClient, exec ServerExecutor, workerID string) *ServerWorker {
	return &ServerWorker{platform: platform, exec: exec, workerID: workerID}
}

// Work executes one River reconcile_server job.
func (w *ServerWorker) Work(ctx context.Context, job *river.Job[ReconcileServerJobArgs]) error {
	err := w.Reconcile(ctx, job.Args.ServerID, job.Args.DesiredRevisionID, job.Attempt)
	if errors.Is(err, errDependencyPending) {
		return river.JobSnooze(5 * time.Second)
	}
	return err
}

var errDependencyPending = errors.New("image publication dependency not yet verified")

// Reconcile claims the execution fence and runs bounded apply or ordered
// retirement checkpoints for one server intent.
func (w *ServerWorker) Reconcile(ctx context.Context, serverID, jobRevisionID string, attempt int) error {
	claim, err := w.platform.ClaimServerIntentFence(ctx, serverID, w.workerID)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	if jobRevisionID != "" && intent.DesiredRevisionID != jobRevisionID {
		_, _ = w.platform.RecordServerIntentReceipt(ctx, serverID, apikit.ServerIntentReceiptRequest{
			DesiredRevisionID: jobRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeSuperseded,
			Attempt:           attempt,
			WorkerID:          w.workerID,
		})
		return nil
	}

	switch intent.Lifecycle {
	case apikit.ServerLifecycleActive:
		return w.reconcileActive(ctx, claim, attempt)
	case apikit.ServerLifecycleRetiring:
		return w.reconcileRetiring(ctx, claim, attempt)
	case apikit.ServerLifecycleRetired:
		_, err := w.platform.RecordServerIntentReceipt(ctx, serverID, apikit.ServerIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeCleaned,
			TargetUID:         intent.TargetCRUID,
			Attempt:           attempt,
			WorkerID:          w.workerID,
		})
		if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unsupported server lifecycle %q", intent.Lifecycle)
	}
}

func (w *ServerWorker) reconcileActive(ctx context.Context, claim apikit.ServerIntentFenceResponse, attempt int) error {
	intent := claim.Intent
	if strings.TrimSpace(intent.PublicationID) != "" {
		pub, found, err := w.platform.GetImagePublicationIntent(ctx, intent.PublicationID)
		if err != nil {
			return err
		}
		if !found || pub.Status != apikit.ImagePublicationStatusPublished || strings.TrimSpace(pub.VerifiedDigest) == "" {
			reason := fmt.Sprintf("waiting for image publication %s to reach Published with verified digest", intent.PublicationID)
			_, _ = w.platform.RecordServerIntentReceipt(ctx, intent.ServerID, apikit.ServerIntentReceiptRequest{
				DesiredRevisionID: intent.DesiredRevisionID,
				FenceEpoch:        claim.FenceEpoch,
				Outcome:           apikit.WorkloadIntentOutcomeBlockedDependency,
				Attempt:           attempt,
				WorkerID:          w.workerID,
				LastErrorClass:    apikit.WorkloadIntentErrorDependencyPending,
				LastError:         reason,
			})
			return fmt.Errorf("%w: %s", errDependencyPending, reason)
		}
	}

	applied, err := w.exec.ApplyMCPServer(ctx, intent)
	if err != nil {
		_, _ = w.platform.RecordServerIntentReceipt(ctx, intent.ServerID, apikit.ServerIntentReceiptRequest{
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

	// Job finishes immediately after apply receipt; rollout readiness is observed
	// independently by workloadintentobserver.
	_, err = w.platform.RecordServerIntentReceipt(ctx, intent.ServerID, apikit.ServerIntentReceiptRequest{
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

func (w *ServerWorker) reconcileRetiring(ctx context.Context, claim apikit.ServerIntentFenceResponse, attempt int) error {
	intent := claim.Intent
	cp := intent.RetirementCheckpoints
	targetUID := intent.TargetCRUID
	if targetUID == "" && intent.Receipt != nil {
		targetUID = intent.Receipt.TargetUID
	}

	persistCheckpoint := func() error {
		updated, err := w.platform.RecordServerRetirementCheckpoint(ctx, intent.ServerID, apikit.ServerRetirementCheckpointRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			WorkerID:          w.workerID,
			TargetCRUID:       targetUID,
			Checkpoints:       cp,
		})
		if err != nil {
			return err
		}
		intent = updated
		cp = updated.RetirementCheckpoints
		if targetUID == "" {
			targetUID = updated.TargetCRUID
		}
		return nil
	}

	recordFail := func(step string, stepErr error) error {
		_, _ = w.platform.RecordServerIntentReceipt(ctx, intent.ServerID, apikit.ServerIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeFailed,
			TargetUID:         targetUID,
			Attempt:           attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.WorkloadIntentErrorTransient,
			LastError:         fmt.Sprintf("retirement step %s failed: %v", step, stepErr),
		})
		return stepErr
	}

	// Ordered retirement checkpoints:
	// 1. retiring marker -> 2. fence/revoke sessions -> 3. retire grants -> 4. clean credentials -> 5. UID-conditional CR delete.
	if !cp.RetiringMarked {
		uid, err := w.exec.MarkRetiring(ctx, intent.Namespace, intent.Name, targetUID)
		if err != nil {
			return recordFail("retiring_marked", err)
		}
		if targetUID == "" && uid != "" {
			targetUID = uid
		}
		cp.RetiringMarked = true
		if err := persistCheckpoint(); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
	}
	if !cp.SessionsFenced {
		if err := w.exec.FenceSessions(ctx, intent.Namespace, intent.Name, targetUID); err != nil {
			return recordFail("sessions_fenced", err)
		}
		cp.SessionsFenced = true
		if err := persistCheckpoint(); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
	}
	if !cp.GrantsRetired {
		if err := w.exec.RetireGrants(ctx, intent.Namespace, intent.Name); err != nil {
			return recordFail("grants_retired", err)
		}
		cp.GrantsRetired = true
		if err := persistCheckpoint(); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
	}
	if !cp.CredentialsCleaned {
		if err := w.exec.CleanCredentials(ctx, intent.Namespace, intent.Name); err != nil {
			return recordFail("credentials_cleaned", err)
		}
		cp.CredentialsCleaned = true
		if err := persistCheckpoint(); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
	}
	if !cp.CRDeleted {
		if err := w.exec.DeleteMCPServerByUID(ctx, intent.Namespace, intent.Name, targetUID); err != nil {
			return recordFail("cr_deleted", err)
		}
		cp.CRDeleted = true
		if err := persistCheckpoint(); err != nil {
			if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
				return nil
			}
			return err
		}
	}

	_, err := w.platform.RecordServerIntentReceipt(ctx, intent.ServerID, apikit.ServerIntentReceiptRequest{
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

// SessionRevoker revokes platform sessions during ordered server retirement.
type SessionRevoker interface {
	RevokeSessions(ctx context.Context, filter sessionstate.Filter, cause string) (int64, error)
}

// KubeServerExecutor implements ServerExecutor against Kubernetes dynamic/core
// clients and k8sstore.
type KubeServerExecutor struct {
	dynamic   dynamic.Interface
	clientset kubernetes.Interface
	accessMgr *k8sstore.Manager
	sessions  SessionRevoker
}

// NewKubeServerExecutor creates a Kubernetes-backed ServerExecutor.
func NewKubeServerExecutor(dyn dynamic.Interface, cs kubernetes.Interface, accessMgr *k8sstore.Manager, sessions SessionRevoker) *KubeServerExecutor {
	return &KubeServerExecutor{dynamic: dyn, clientset: cs, accessMgr: accessMgr, sessions: sessions}
}

func (e *KubeServerExecutor) ApplyMCPServer(ctx context.Context, intent apikit.ServerIntent) (ApplyResult, error) {
	var spec mcpv1alpha1.MCPServerSpec
	if len(intent.SpecJSON) > 0 {
		if err := json.Unmarshal(intent.SpecJSON, &spec); err != nil {
			return ApplyResult{}, fmt.Errorf("decode server spec: %w", err)
		}
	}
	if intent.TeamID != "" && spec.TeamID == "" {
		spec.TeamID = intent.TeamID
	}
	desired := &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{APIVersion: mcpv1alpha1.Group + "/" + mcpv1alpha1.Version, Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.Name,
			Namespace: intent.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mcp-runtime",
				"mcpruntime.org/server-id":     intent.ServerID,
			},
			Annotations: map[string]string{
				"mcpruntime.org/desired-revision-id": intent.DesiredRevisionID,
			},
		},
		Spec: spec,
	}
	rawObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return ApplyResult{}, err
	}
	u := &unstructured.Unstructured{Object: rawObj}
	res := e.dynamic.Resource(controlplane.MCPServerGVR).Namespace(intent.Namespace)

	existing, err := res.Get(ctx, intent.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, createErr := res.Create(ctx, u, metav1.CreateOptions{})
		if createErr != nil {
			return ApplyResult{}, createErr
		}
		return ApplyResult{UID: string(created.GetUID()), Generation: created.GetGeneration()}, nil
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if existing.GetAnnotations()[mcpaccess.ServerRetiringAnnotation] == "true" {
		return ApplyResult{}, fmt.Errorf("server %s/%s is currently retiring", intent.Namespace, intent.Name)
	}
	u.SetResourceVersion(existing.GetResourceVersion())
	u.SetUID(existing.GetUID())
	u.SetFinalizers(existing.GetFinalizers())
	u.SetOwnerReferences(existing.GetOwnerReferences())
	mergedLabels := map[string]string{}
	for k, v := range existing.GetLabels() {
		mergedLabels[k] = v
	}
	for k, v := range u.GetLabels() {
		mergedLabels[k] = v
	}
	u.SetLabels(mergedLabels)
	mergedAnn := map[string]string{}
	for k, v := range existing.GetAnnotations() {
		mergedAnn[k] = v
	}
	for k, v := range u.GetAnnotations() {
		mergedAnn[k] = v
	}
	u.SetAnnotations(mergedAnn)

	updated, err := res.Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{UID: string(updated.GetUID()), Generation: updated.GetGeneration()}, nil
}

func (e *KubeServerExecutor) MarkRetiring(ctx context.Context, namespace, name, targetCRUID string) (string, error) {
	res := e.dynamic.Resource(controlplane.MCPServerGVR).Namespace(namespace)
	current, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return targetCRUID, nil
	}
	if err != nil {
		return "", err
	}
	liveUID := string(current.GetUID())
	if targetCRUID != "" && liveUID != targetCRUID {
		// A replacement CR with a new UID now occupies this name; do not mark it retiring.
		return targetCRUID, nil
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"uid":             current.GetUID(),
			"resourceVersion": current.GetResourceVersion(),
			"annotations": map[string]string{
				mcpaccess.ServerRetiringAnnotation: "true",
			},
		},
	})
	if _, err := res.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	return liveUID, nil
}

func (e *KubeServerExecutor) FenceSessions(ctx context.Context, namespace, name, targetCRUID string) error {
	if e.sessions == nil {
		return nil
	}
	if targetCRUID != "" {
		if _, err := e.sessions.RevokeSessions(ctx, sessionstate.Filter{ServerUID: targetCRUID}, "server_retired"); err != nil {
			return err
		}
		return nil
	}
	_, err := e.sessions.RevokeSessions(ctx, sessionstate.Filter{Namespace: namespace, ServerName: name}, "server_retired")
	return err
}

func (e *KubeServerExecutor) RetireGrants(ctx context.Context, namespace, name string) error {
	if e.accessMgr == nil {
		return nil
	}
	return e.accessMgr.RetireServerAccess(ctx, namespace, name)
}

func (e *KubeServerExecutor) CleanCredentials(ctx context.Context, namespace, name string) error {
	if e.clientset == nil {
		return nil
	}
	secretName := name + "-analytics"
	secrets := e.clientset.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		return nil
	}
	if owner := secret.Annotations["mcpruntime.org/credential-server"]; owner != "" && owner != name {
		return nil
	}
	uid := secret.UID
	err = secrets.Delete(ctx, secretName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (e *KubeServerExecutor) DeleteMCPServerByUID(ctx context.Context, namespace, name, targetCRUID string) error {
	res := e.dynamic.Resource(controlplane.MCPServerGVR).Namespace(namespace)
	if targetCRUID == "" {
		return nil
	}
	uid := types.UID(targetCRUID)
	err := res.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		// Already gone or UID belongs to a replacement CR — never delete replacement.
		return nil
	}
	return err
}
