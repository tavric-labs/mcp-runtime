package accessintentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/riverqueue/river"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"mcp-runtime-api/internal/platformapi"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/apikit"
)

var grantGVR = schema.GroupVersionResource{
	Group:    mcpv1alpha1.Group,
	Version:  mcpv1alpha1.Version,
	Resource: mcpv1alpha1.MCPAccessGrantResource,
}

// GrantPlatformClient is the Platform API surface used by GrantWorker.
type GrantPlatformClient interface {
	ClaimGrantIntentFence(ctx context.Context, grantID, workerID string) (apikit.GrantIntentFenceResponse, error)
	GetGrantIntent(ctx context.Context, grantID string) (apikit.GrantIntent, bool, error)
	RecordGrantIntentReceipt(ctx context.Context, grantID string, req apikit.GrantIntentReceiptRequest) (apikit.GrantReceipt, error)
}

// ProjectedGrant is the Kubernetes identity metadata observed after applying an MCPAccessGrant CR.
type ProjectedGrant struct {
	UID        string
	Generation int64
	SpecDigest string
}

// GrantProjector projects durable grant intents onto Kubernetes MCPAccessGrant CRs.
type GrantProjector interface {
	ApplyGrant(ctx context.Context, intent apikit.GrantIntent) (ProjectedGrant, error)
	DeleteGrantByUID(ctx context.Context, namespace, name, expectedUID, expectedGrantID string) (bool, error)
}

// ErrGrantOwnershipConflict indicates a Kubernetes CR belongs to a different grant incarnation.
var ErrGrantOwnershipConflict = errors.New("grant CR owned by different incarnation")

// GrantWorker reconciles durable grant intents onto Kubernetes MCPAccessGrant resources.
type GrantWorker struct {
	river.WorkerDefaults[ReconcileGrantJob]
	Platform  GrantPlatformClient
	Projector GrantProjector
	WorkerID  string
}

func (w *GrantWorker) workerID() string {
	if strings.TrimSpace(w.WorkerID) != "" {
		return strings.TrimSpace(w.WorkerID)
	}
	return "runtime-access-grant-worker"
}

func (w *GrantWorker) Work(ctx context.Context, job *river.Job[ReconcileGrantJob]) error {
	ctx = contextWithJobTrace(ctx, job.Args.RequestID, job.Args.Traceparent, job.Args.Tracestate)
	attempt := int(job.Attempt)

	claim, err := w.Platform.ClaimGrantIntentFence(ctx, job.Args.GrantID, w.workerID())
	if err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	epoch := claim.FenceEpoch

	switch intent.State {
	case apikit.GrantIntentStateActive:
		return w.reconcileActive(ctx, intent, epoch, attempt)
	case apikit.GrantIntentStateRevoked, apikit.GrantIntentStateDeleted:
		return w.reconcileRevoked(ctx, intent, epoch, attempt)
	default:
		_, _ = w.Platform.RecordGrantIntentReceipt(ctx, intent.GrantID, apikit.GrantIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        epoch,
			Outcome:           apikit.AccessIntentOutcomeTerminal,
			Attempt:           attempt,
			WorkerID:          w.workerID(),
			LastErrorClass:    apikit.AccessIntentErrorInvalidConfig,
			LastError:         fmt.Sprintf("unsupported grant intent state %q", intent.State),
		})
		return river.JobCancel(fmt.Errorf("unsupported grant intent state %q", intent.State))
	}
}

func (w *GrantWorker) reconcileActive(ctx context.Context, intent apikit.GrantIntent, epoch int64, attempt int) error {
	projected, err := w.Projector.ApplyGrant(ctx, intent)
	if err != nil {
		return w.handleGrantError(ctx, intent, epoch, attempt, err)
	}

	// Post-apply check: if the grant was revoked or deleted while applying,
	// immediately delete the CR with UID precondition and NEVER record Applied.
	latest, found, err := w.Platform.GetGrantIntent(ctx, intent.GrantID)
	if err != nil {
		return err
	}
	if !found || latest.State != apikit.GrantIntentStateActive || latest.DesiredRevisionID != intent.DesiredRevisionID {
		if !found || latest.State != apikit.GrantIntentStateActive {
			if _, delErr := w.Projector.DeleteGrantByUID(ctx, intent.Namespace, intent.Name, projected.UID, intent.GrantID); delErr != nil {
				return fmt.Errorf("compensating delete after mid-apply revocation: %w", delErr)
			}
		}
		return nil
	}

	_, err = w.Platform.RecordGrantIntentReceipt(ctx, intent.GrantID, apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.AccessIntentOutcomeApplied,
		TargetUID:         projected.UID,
		TargetGeneration:  projected.Generation,
		SpecDigest:        projected.SpecDigest,
		Attempt:           attempt,
		WorkerID:          w.workerID(),
	})
	if err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentConflict) {
			// A newer fence or revision superseded this worker; never overwrite newer state.
			return nil
		}
		return err
	}
	return nil
}

func (w *GrantWorker) reconcileRevoked(ctx context.Context, intent apikit.GrantIntent, epoch int64, attempt int) error {
	expectedUID := ""
	if intent.Receipt != nil {
		expectedUID = intent.Receipt.TargetUID
	}
	if _, err := w.Projector.DeleteGrantByUID(ctx, intent.Namespace, intent.Name, expectedUID, intent.GrantID); err != nil {
		return w.handleGrantError(ctx, intent, epoch, attempt, err)
	}
	_, err := w.Platform.RecordGrantIntentReceipt(ctx, intent.GrantID, apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.AccessIntentOutcomeCleaned,
		TargetUID:         expectedUID,
		SpecDigest:        SpecDigest(intent.SpecJSON),
		Attempt:           attempt,
		WorkerID:          w.workerID(),
	})
	if err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentConflict) {
			return nil
		}
		return err
	}
	return nil
}

func (w *GrantWorker) handleGrantError(ctx context.Context, intent apikit.GrantIntent, epoch int64, attempt int, err error) error {
	errClass := apikit.AccessIntentErrorTransient
	outcome := apikit.AccessIntentOutcomeFailed
	terminal := false
	if errors.Is(err, ErrGrantOwnershipConflict) {
		errClass = apikit.AccessIntentErrorOwnershipConflict
		outcome = apikit.AccessIntentOutcomeTerminal
		terminal = true
	}
	_, _ = w.Platform.RecordGrantIntentReceipt(ctx, intent.GrantID, apikit.GrantIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           outcome,
		Attempt:           attempt,
		WorkerID:          w.workerID(),
		LastErrorClass:    errClass,
		LastError:         err.Error(),
	})
	if terminal {
		return river.JobCancel(err)
	}
	return err
}

// K8sGrantProjector applies and UID-conditionally deletes MCPAccessGrant CRs via dynamic.Interface.
type K8sGrantProjector struct {
	Dynamic dynamic.Interface
}

func (p *K8sGrantProjector) ApplyGrant(ctx context.Context, intent apikit.GrantIntent) (ProjectedGrant, error) {
	if p == nil || p.Dynamic == nil {
		return ProjectedGrant{}, errors.New("kubernetes dynamic client unavailable")
	}
	var spec map[string]any
	if len(intent.SpecJSON) > 0 {
		if err := json.Unmarshal(intent.SpecJSON, &spec); err != nil {
			return ProjectedGrant{}, fmt.Errorf("decode grant spec_json: %w", err)
		}
	}
	if spec == nil {
		spec = map[string]any{}
	}
	digest := SpecDigest(intent.SpecJSON)
	annotations := map[string]any{
		mcpaccess.ServerUIDAnnotation: intent.ServerUID,
		GrantIDAnnotation:             intent.GrantID,
		DesiredRevisionAnnotation:     intent.DesiredRevisionID,
		SpecDigestAnnotation:          digest,
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": mcpv1alpha1.Group + "/" + mcpv1alpha1.Version,
		"kind":       "MCPAccessGrant",
		"metadata": map[string]any{
			"name":        intent.Name,
			"namespace":   intent.Namespace,
			"annotations": annotations,
		},
		"spec": spec,
	}}

	res := p.Dynamic.Resource(grantGVR).Namespace(intent.Namespace)
	existing, err := res.Get(ctx, intent.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, createErr := res.Create(ctx, obj, metav1.CreateOptions{})
		if createErr != nil {
			return ProjectedGrant{}, createErr
		}
		return ProjectedGrant{
			UID:        string(created.GetUID()),
			Generation: created.GetGeneration(),
			SpecDigest: digest,
		}, nil
	}
	if err != nil {
		return ProjectedGrant{}, err
	}
	if ownerID := existing.GetAnnotations()[GrantIDAnnotation]; ownerID != "" && ownerID != intent.GrantID {
		return ProjectedGrant{}, fmt.Errorf("%w: existing grant %s/%s belongs to %s", ErrGrantOwnershipConflict, intent.Namespace, intent.Name, ownerID)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	updated, err := res.Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return ProjectedGrant{}, err
	}
	return ProjectedGrant{
		UID:        string(updated.GetUID()),
		Generation: updated.GetGeneration(),
		SpecDigest: digest,
	}, nil
}

func (p *K8sGrantProjector) DeleteGrantByUID(ctx context.Context, namespace, name, expectedUID, expectedGrantID string) (bool, error) {
	if p == nil || p.Dynamic == nil {
		return false, errors.New("kubernetes dynamic client unavailable")
	}
	res := p.Dynamic.Resource(grantGVR).Namespace(namespace)
	existing, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expectedGrantID != "" {
		if ownerID := existing.GetAnnotations()[GrantIDAnnotation]; ownerID != "" && ownerID != expectedGrantID {
			return false, nil
		}
	}
	uid := existing.GetUID()
	if expectedUID != "" && string(uid) != expectedUID {
		return false, nil
	}
	preconditions := &metav1.Preconditions{UID: (*types.UID)(&uid)}
	if err := res.Delete(ctx, name, metav1.DeleteOptions{Preconditions: preconditions}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
