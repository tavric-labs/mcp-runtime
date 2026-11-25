package accessintentworker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riverqueue/river"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
)

var certificateRequestGVR = schema.GroupVersionResource{
	Group:    "cert-manager.io",
	Version:  "v1",
	Resource: "certificaterequests",
}

// EnrollmentPlatformClient is the Platform API surface used by EnrollmentWorker.
type EnrollmentPlatformClient interface {
	ClaimEnrollmentIntentFence(ctx context.Context, opID, workerID string) (apikit.EnrollmentIntentFenceResponse, error)
	RecordEnrollmentIntentReceipt(ctx context.Context, opID string, req apikit.EnrollmentIntentReceiptRequest) (apikit.EnrollmentReceipt, error)
	ActivateEnrollmentIntent(ctx context.Context, opID string, req apikit.EnrollmentIntentActivateRequest) (apikit.EnrollmentIntent, error)
}

// CertificateRequestSpec describes the deterministic cert-manager CertificateRequest to submit.
type CertificateRequestSpec struct {
	Namespace   string
	Name        string
	SessionName string
	OperationID string
	IssuerName  string
	CSRPEM      string
	Duration    time.Duration
}

// CertificateRequestState is the observed status of a Kubernetes CertificateRequest.
type CertificateRequestState struct {
	Name          string
	UID           string
	Issued        bool
	Pending       bool
	Failed        bool
	FailureReason string
	Serial        string
	Fingerprint   string
}

// CertificateRequestManager inspects and creates deterministic CertificateRequest objects.
type CertificateRequestManager interface {
	GetCertificateRequest(ctx context.Context, namespace, name string) (*CertificateRequestState, bool, error)
	CreateCertificateRequest(ctx context.Context, spec CertificateRequestSpec) (*CertificateRequestState, error)
}

// CSRProvider resolves an in-flight PEM CSR for submission (kept out of Postgres).
type CSRProvider func(ctx context.Context, intent apikit.EnrollmentIntent) (csrPEM string, duration time.Duration, err error)

// EnrollmentWorker reconciles durable adapter certificate enrollment operations.
type EnrollmentWorker struct {
	river.WorkerDefaults[ReconcileEnrollmentJob]
	Platform      EnrollmentPlatformClient
	CRManager     CertificateRequestManager
	CSRProvider   CSRProvider
	ClusterIssuer string
	WorkerID      string
	SnoozeDelay   time.Duration
}

func (w *EnrollmentWorker) workerID() string {
	if strings.TrimSpace(w.WorkerID) != "" {
		return strings.TrimSpace(w.WorkerID)
	}
	return "runtime-access-enrollment-worker"
}

func (w *EnrollmentWorker) snoozeDuration() time.Duration {
	if w.SnoozeDelay > 0 {
		return w.SnoozeDelay
	}
	return 2 * time.Second
}

func (w *EnrollmentWorker) Work(ctx context.Context, job *river.Job[ReconcileEnrollmentJob]) error {
	ctx = contextWithJobTrace(ctx, job.Args.RequestID, job.Args.Traceparent, job.Args.Tracestate)
	attempt := int(job.Attempt)

	claim, err := w.Platform.ClaimEnrollmentIntentFence(ctx, job.Args.OperationID, w.workerID())
	if err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	epoch := claim.FenceEpoch

	switch intent.State {
	case apikit.EnrollmentStateActivated, apikit.EnrollmentStateRevoked, apikit.EnrollmentStateSuperseded, apikit.EnrollmentStateFailed:
		return nil
	}

	// Inspect the deterministic CertificateRequest BEFORE creating so crashes
	// after creation resume the existing object and never create duplicates.
	crState, exists, err := w.CRManager.GetCertificateRequest(ctx, intent.Namespace, intent.CertificateRequestName)
	if err != nil {
		return w.recordTransientFailure(ctx, intent, epoch, attempt, err)
	}
	if !exists {
		if w.CSRProvider == nil {
			err := errors.New("csr provider unavailable for unsubmitted enrollment")
			return w.recordTransientFailure(ctx, intent, epoch, attempt, err)
		}
		csrPEM, duration, csrErr := w.CSRProvider(ctx, intent)
		if csrErr != nil {
			return w.recordTransientFailure(ctx, intent, epoch, attempt, csrErr)
		}
		crState, err = w.CRManager.CreateCertificateRequest(ctx, CertificateRequestSpec{
			Namespace:   intent.Namespace,
			Name:        intent.CertificateRequestName,
			SessionName: intent.SessionName,
			OperationID: intent.OperationID,
			IssuerName:  w.ClusterIssuer,
			CSRPEM:      csrPEM,
			Duration:    duration,
		})
		if err != nil {
			return w.recordTransientFailure(ctx, intent, epoch, attempt, err)
		}
	}

	if intent.State == apikit.EnrollmentStatePending && !crState.Issued && !crState.Failed {
		if _, recErr := w.Platform.RecordEnrollmentIntentReceipt(ctx, intent.OperationID, apikit.EnrollmentIntentReceiptRequest{
			DesiredRevisionID:      intent.DesiredRevisionID,
			FenceEpoch:             epoch,
			State:                  apikit.EnrollmentStateSubmitted,
			Outcome:                apikit.AccessIntentOutcomeApplied,
			CertificateRequestName: crState.Name,
			CertificateRequestUID:  crState.UID,
			Attempt:                attempt,
			WorkerID:               w.workerID(),
		}); recErr != nil && !errors.Is(recErr, platformapi.ErrAccessIntentConflict) {
			return recErr
		}
	}

	if crState.Failed {
		_, _ = w.Platform.RecordEnrollmentIntentReceipt(ctx, intent.OperationID, apikit.EnrollmentIntentReceiptRequest{
			DesiredRevisionID:      intent.DesiredRevisionID,
			FenceEpoch:             epoch,
			State:                  apikit.EnrollmentStateFailed,
			Outcome:                apikit.AccessIntentOutcomeTerminal,
			CertificateRequestName: crState.Name,
			CertificateRequestUID:  crState.UID,
			Attempt:                attempt,
			WorkerID:               w.workerID(),
			LastErrorClass:         apikit.AccessIntentErrorInvalidConfig,
			LastError:              crState.FailureReason,
		})
		return river.JobCancel(fmt.Errorf("certificate request failed: %s", crState.FailureReason))
	}

	if !crState.Issued {
		return river.JobSnooze(w.snoozeDuration())
	}

	// Record Issued state before conditional activation (idempotent if retried).
	if _, recErr := w.Platform.RecordEnrollmentIntentReceipt(ctx, intent.OperationID, apikit.EnrollmentIntentReceiptRequest{
		DesiredRevisionID:      intent.DesiredRevisionID,
		FenceEpoch:             epoch,
		State:                  apikit.EnrollmentStateIssued,
		Outcome:                apikit.AccessIntentOutcomeApplied,
		CertificateRequestName: crState.Name,
		CertificateRequestUID:  crState.UID,
		Attempt:                attempt,
		WorkerID:               w.workerID(),
	}); recErr != nil {
		if errors.Is(recErr, platformapi.ErrAccessIntentConflict) {
			return river.JobCancel(recErr)
		}
		return recErr
	}

	// Post-issuance conditional activation in Platform API re-verifies that the
	// session is still active & non-revoked, agent is active, and server UID matches.
	if _, err := w.Platform.ActivateEnrollmentIntent(ctx, intent.OperationID, apikit.EnrollmentIntentActivateRequest{
		DesiredRevisionID:      intent.DesiredRevisionID,
		FenceEpoch:             epoch,
		WorkerID:               w.workerID(),
		CertificateRequestName: crState.Name,
		CertificateRequestUID:  crState.UID,
		Serial:                 crState.Serial,
		Fingerprint:            crState.Fingerprint,
	}); err != nil {
		if errors.Is(err, platformapi.ErrAccessIntentConflict) {
			return river.JobCancel(err)
		}
		return err
	}
	return nil
}

func (w *EnrollmentWorker) recordTransientFailure(ctx context.Context, intent apikit.EnrollmentIntent, epoch int64, attempt int, err error) error {
	_, _ = w.Platform.RecordEnrollmentIntentReceipt(ctx, intent.OperationID, apikit.EnrollmentIntentReceiptRequest{
		DesiredRevisionID:      intent.DesiredRevisionID,
		FenceEpoch:             epoch,
		Outcome:                apikit.AccessIntentOutcomeFailed,
		CertificateRequestName: intent.CertificateRequestName,
		Attempt:                attempt,
		WorkerID:               w.workerID(),
		LastErrorClass:         apikit.AccessIntentErrorTransient,
		LastError:              err.Error(),
	})
	return err
}

// K8sCertificateRequestManager manages deterministic cert-manager CertificateRequests.
type K8sCertificateRequestManager struct {
	Dynamic dynamic.Interface
}

func (m *K8sCertificateRequestManager) GetCertificateRequest(ctx context.Context, namespace, name string) (*CertificateRequestState, bool, error) {
	if m == nil || m.Dynamic == nil {
		return nil, false, errors.New("kubernetes dynamic client unavailable")
	}
	obj, err := m.Dynamic.Resource(certificateRequestGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return parseCertificateRequestState(obj), true, nil
}

func (m *K8sCertificateRequestManager) CreateCertificateRequest(ctx context.Context, spec CertificateRequestSpec) (*CertificateRequestState, error) {
	if m == nil || m.Dynamic == nil {
		return nil, errors.New("kubernetes dynamic client unavailable")
	}
	if strings.TrimSpace(spec.IssuerName) == "" {
		return nil, errors.New("workload certificate issuer is not configured")
	}
	duration := spec.Duration
	if duration <= 0 {
		duration = time.Hour
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "CertificateRequest",
		"metadata": map[string]any{
			"name":      spec.Name,
			"namespace": spec.Namespace,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "mcp-runtime",
				"mcpruntime.org/session":       spec.SessionName,
			},
			"annotations": map[string]any{
				"mcpruntime.org/enrollment-operation-id": spec.OperationID,
			},
		},
		"spec": map[string]any{
			"request":  base64.StdEncoding.EncodeToString([]byte(spec.CSRPEM)),
			"duration": duration.Round(time.Second).String(),
			"usages":   []any{"digital signature", "client auth"},
			"issuerRef": map[string]any{
				"group": "cert-manager.io",
				"kind":  "ClusterIssuer",
				"name":  spec.IssuerName,
			},
		},
	}}
	created, err := m.Dynamic.Resource(certificateRequestGVR).Namespace(spec.Namespace).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, found, getErr := m.GetCertificateRequest(ctx, spec.Namespace, spec.Name)
		if getErr != nil {
			return nil, getErr
		}
		if !found {
			return nil, err
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	return parseCertificateRequestState(created), nil
}

func parseCertificateRequestState(obj *unstructured.Unstructured) *CertificateRequestState {
	st := &CertificateRequestState{
		Name: obj.GetName(),
		UID:  string(obj.GetUID()),
	}
	cert, _, _ := unstructured.NestedString(obj.Object, "status", "certificate")
	if strings.TrimSpace(cert) != "" {
		st.Issued = true
		return st
	}
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if found {
		for _, raw := range conditions {
			cond, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			cType, _, _ := unstructured.NestedString(cond, "type")
			cStatus, _, _ := unstructured.NestedString(cond, "status")
			reason, _, _ := unstructured.NestedString(cond, "reason")
			msg, _, _ := unstructured.NestedString(cond, "message")
			if cType == "Ready" && cStatus == "False" && (reason == "Failed" || reason == "Denied") {
				st.Failed = true
				st.FailureReason = strings.TrimSpace(reason + " " + msg)
				return st
			}
		}
	}
	st.Pending = true
	return st
}
