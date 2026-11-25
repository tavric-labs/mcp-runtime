package workloadintentworker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/riverqueue/river"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/registrypush"
)

// ImagePublicationPlatformClient defines Platform API calls for ImagePublicationWorker.
type ImagePublicationPlatformClient interface {
	ClaimImagePublicationFence(ctx context.Context, publicationID, workerID string) (apikit.ImagePublicationFenceResponse, error)
	RecordImagePublicationReceipt(ctx context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error)
}

// HelperState summarizes the observed phase of a deterministic push helper Pod.
type HelperState string

const (
	HelperStateNotFound  HelperState = "NotFound"
	HelperStateRunning   HelperState = "Running"
	HelperStateSucceeded HelperState = "Succeeded"
	HelperStateFailed    HelperState = "Failed"
)

// PublicationHelperRunner manages resumable deterministic helper Pods and
// verifies the resulting target manifest digest directly against the registry.
type PublicationHelperRunner interface {
	InspectHelper(ctx context.Context, namespace, helperName string) (HelperState, error)
	CreateHelper(ctx context.Context, intent apikit.ImagePublicationIntent, helperName string) error
	WaitHelperSucceeded(ctx context.Context, namespace, helperName string) error
	VerifyRegistryDigest(ctx context.Context, intent apikit.ImagePublicationIntent) (string, error)
}

var helperUnsafeChars = regexp.MustCompile(`[^a-z0-9-]+`)

// DeterministicHelperName derives a DNS-1123 compliant helper Pod name tied to
// publicationID and attempt so worker crash recovery can inspect and resume an
// existing helper instead of spawning duplicate helpers.
func DeterministicHelperName(publicationID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	clean := strings.ToLower(strings.TrimSpace(publicationID))
	clean = helperUnsafeChars.ReplaceAllString(clean, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "pub"
	}
	suffix := fmt.Sprintf("-a%d", attempt)
	const prefix = "mcp-push-"
	maxBody := 63 - len(prefix) - len(suffix)
	if len(clean) > maxBody {
		clean = strings.TrimRight(clean[:maxBody], "-")
	}
	return prefix + clean + suffix
}

// ImagePublicationWorker reconciles finalized staged image archive publications
// on the dedicated registry_publication queue.
type ImagePublicationWorker struct {
	river.WorkerDefaults[ReconcileImagePublicationJobArgs]
	platform ImagePublicationPlatformClient
	runner   PublicationHelperRunner
	workerID string
}

// NewImagePublicationWorker constructs an ImagePublicationWorker.
func NewImagePublicationWorker(platform ImagePublicationPlatformClient, runner PublicationHelperRunner, workerID string) *ImagePublicationWorker {
	return &ImagePublicationWorker{platform: platform, runner: runner, workerID: workerID}
}

// Work executes one River reconcile_image_publication job.
func (w *ImagePublicationWorker) Work(ctx context.Context, job *river.Job[ReconcileImagePublicationJobArgs]) error {
	err := w.Reconcile(ctx, job.Args.PublicationID, job.Args.DesiredRevisionID, job.Attempt)
	if errors.Is(err, errUnfinalizedUpload) {
		return river.JobCancel(err)
	}
	return err
}

var errUnfinalizedUpload = errors.New("image publication upload is not finalized")

// Reconcile claims the fence, inspects/resumes or creates the deterministic
// helper Pod, verifies the target manifest digest at the registry, and records
// the fenced receipt.
func (w *ImagePublicationWorker) Reconcile(ctx context.Context, publicationID, jobRevisionID string, attempt int) error {
	claim, err := w.platform.ClaimImagePublicationFence(ctx, publicationID, w.workerID)
	if err != nil {
		if errors.Is(err, platformapi.ErrWorkloadIntentNotFound) {
			return nil
		}
		return err
	}
	intent := claim.Intent
	if intent.UploadState != apikit.ImageUploadStateFinalized || strings.TrimSpace(intent.ArtifactRef) == "" {
		_, _ = w.platform.RecordImagePublicationReceipt(ctx, publicationID, apikit.ImagePublicationReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        claim.FenceEpoch,
			Outcome:           apikit.WorkloadIntentOutcomeTerminal,
			Status:            apikit.ImagePublicationStatusFailed,
			Attempt:           attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.WorkloadIntentErrorInvalidConfig,
			LastError:         "cannot publish unfinalized staged upload",
		})
		return errUnfinalizedUpload
	}
	if intent.Status == apikit.ImagePublicationStatusPublished && strings.TrimSpace(intent.VerifiedDigest) != "" {
		return nil
	}

	helperName := strings.TrimSpace(intent.HelperName)
	if helperName == "" {
		helperName = DeterministicHelperName(intent.PublicationID, attempt)
	}

	state, err := w.runner.InspectHelper(ctx, intent.Namespace, helperName)
	if err != nil {
		return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
	}

	switch state {
	case HelperStateRunning:
		// Worker crash recovery: resume watching the already running helper instead of spawning a duplicate.
		if err := w.runner.WaitHelperSucceeded(ctx, intent.Namespace, helperName); err != nil {
			return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
		}
	case HelperStateSucceeded:
		// Helper already finished; proceed directly to registry manifest digest verification.
	case HelperStateFailed:
		// Prior helper failed; allocate a fresh deterministic helper name for this attempt.
		helperName = DeterministicHelperName(intent.PublicationID, attempt+1)
		if err := w.runner.CreateHelper(ctx, intent, helperName); err != nil {
			return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
		}
		if err := w.runner.WaitHelperSucceeded(ctx, intent.Namespace, helperName); err != nil {
			return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
		}
	case HelperStateNotFound:
		if err := w.runner.CreateHelper(ctx, intent, helperName); err != nil {
			return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
		}
		if err := w.runner.WaitHelperSucceeded(ctx, intent.Namespace, helperName); err != nil {
			return w.recordTransientFailure(ctx, claim, helperName, attempt, err)
		}
	}

	// Helper completion alone is NEVER sufficient: verify target manifest digest at registry.
	digest, err := w.runner.VerifyRegistryDigest(ctx, intent)
	digest = strings.TrimSpace(digest)
	if err != nil || digest == "" {
		if err == nil {
			err = errors.New("registry returned empty manifest digest")
		}
		return w.recordTransientFailure(ctx, claim, helperName, attempt, fmt.Errorf("verify registry manifest digest: %w", err))
	}

	_, err = w.platform.RecordImagePublicationReceipt(ctx, publicationID, apikit.ImagePublicationReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        claim.FenceEpoch,
		Outcome:           apikit.WorkloadIntentOutcomeApplied,
		Status:            apikit.ImagePublicationStatusPublished,
		HelperName:        helperName,
		VerifiedDigest:    digest,
		Attempt:           attempt,
		WorkerID:          w.workerID,
	})
	if errors.Is(err, platformapi.ErrWorkloadIntentConflict) {
		return nil
	}
	return err
}

func (w *ImagePublicationWorker) recordTransientFailure(ctx context.Context, claim apikit.ImagePublicationFenceResponse, helperName string, attempt int, cause error) error {
	_, _ = w.platform.RecordImagePublicationReceipt(ctx, claim.Intent.PublicationID, apikit.ImagePublicationReceiptRequest{
		DesiredRevisionID: claim.Intent.DesiredRevisionID,
		FenceEpoch:        claim.FenceEpoch,
		Outcome:           apikit.WorkloadIntentOutcomeFailed,
		Status:            apikit.ImagePublicationStatusPublishing,
		HelperName:        helperName,
		Attempt:           attempt,
		WorkerID:          w.workerID,
		LastErrorClass:    apikit.WorkloadIntentErrorTransient,
		LastError:         cause.Error(),
	})
	return cause
}

// KubePublicationHelperRunner runs deterministic skopeo helper Pods in the
// trusted helper namespace and verifies resulting manifest digests via HTTP HEAD.
type KubePublicationHelperRunner struct {
	clientset       kubernetes.Interface
	helperNamespace string
	skopeoImage     string
	stagingMount    string
	httpClient      *http.Client
	waitTimeout     time.Duration
}

// NewKubePublicationHelperRunner creates a Kubernetes-backed helper runner.
func NewKubePublicationHelperRunner(cs kubernetes.Interface, helperNamespace, skopeoImage, stagingMount string) *KubePublicationHelperRunner {
	if strings.TrimSpace(helperNamespace) == "" {
		helperNamespace = "registry"
	}
	if strings.TrimSpace(skopeoImage) == "" {
		skopeoImage = "quay.io/skopeo/stable:v1.14"
	}
	if strings.TrimSpace(stagingMount) == "" {
		stagingMount = "/var/lib/mcp-runtime/staging"
	}
	return &KubePublicationHelperRunner{
		clientset:       cs,
		helperNamespace: helperNamespace,
		skopeoImage:     skopeoImage,
		stagingMount:    stagingMount,
		httpClient:      &http.Client{Timeout: 15 * time.Second},
		waitTimeout:     5 * time.Minute,
	}
}

func (r *KubePublicationHelperRunner) InspectHelper(ctx context.Context, _ string, helperName string) (HelperState, error) {
	pod, err := r.clientset.CoreV1().Pods(r.helperNamespace).Get(ctx, helperName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return HelperStateNotFound, nil
	}
	if err != nil {
		return HelperStateNotFound, err
	}
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodRunning:
		return HelperStateRunning, nil
	case corev1.PodSucceeded:
		return HelperStateSucceeded, nil
	default:
		return HelperStateFailed, nil
	}
}

func (r *KubePublicationHelperRunner) CreateHelper(ctx context.Context, intent apikit.ImagePublicationIntent, helperName string) error {
	target := fmt.Sprintf("docker://%s/%s:%s", strings.TrimSuffix(intent.Registry, "/"), strings.TrimPrefix(intent.Repository, "/"), intent.Tag)
	source := fmt.Sprintf("docker-archive:%s/%s", strings.TrimSuffix(r.stagingMount, "/"), strings.TrimPrefix(intent.ArtifactRef, "/"))
	automount := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helperName,
			Namespace: r.helperNamespace,
			Labels: map[string]string{
				registrypush.HelperLabelKey:     registrypush.HelperLabelValue,
				"mcpruntime.org/publication-id": intent.PublicationID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &automount,
			SecurityContext:              kubeworkload.RestrictedPodSecurityContext(),
			Containers: []corev1.Container{{
				Name:            "skopeo",
				Image:           r.skopeoImage,
				Command:         []string{"skopeo", "copy", "--dest-tls-verify=false", source, target},
				SecurityContext: kubeworkload.RestrictedContainerSecurityContext(),
			}},
		},
	}
	_, err := r.clientset.CoreV1().Pods(r.helperNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (r *KubePublicationHelperRunner) WaitHelperSucceeded(ctx context.Context, _ string, helperName string) error {
	waitCtx, cancel := context.WithTimeout(ctx, r.waitTimeout)
	defer cancel()
	return wait.PollUntilContextCancel(waitCtx, time.Second, true, func(ctx context.Context) (bool, error) {
		pod, err := r.clientset.CoreV1().Pods(r.helperNamespace).Get(ctx, helperName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return true, nil
		case corev1.PodFailed:
			return false, fmt.Errorf("helper pod %s/%s failed", r.helperNamespace, helperName)
		default:
			return false, nil
		}
	})
}

func (r *KubePublicationHelperRunner) VerifyRegistryDigest(ctx context.Context, intent apikit.ImagePublicationIntent) (string, error) {
	scheme := "https"
	if strings.HasPrefix(intent.Registry, "localhost") || strings.Contains(intent.Registry, ".svc") || strings.HasSuffix(intent.Registry, ":5000") {
		scheme = "http"
	}
	endpoint := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, strings.TrimSuffix(intent.Registry, "/"), strings.Trim(intent.Repository, "/"), intent.Tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry manifest head returned status %d", resp.StatusCode)
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return "", errors.New("registry response missing Docker-Content-Digest header")
	}
	return digest, nil
}
