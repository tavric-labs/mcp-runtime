// Package accessintentobserver runs an independent status observer for durable
// access-control grant intents. It reads live Kubernetes MCPAccessGrant CRs and
// serving gateway replica status endpoints, reports fenced observations with a
// monotonic observer_epoch, and never creates desired revisions or River jobs.
package accessintentobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime-api/internal/accessintentworker"
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

// ErrStaleObserverEpoch is returned when Platform API rejects an observation
// because a newer observer holds the cluster lease.
var ErrStaleObserverEpoch = errors.New("observer epoch superseded by newer lease holder")

// PlatformClient is the read + observation-only Platform API surface.
// It deliberately omits intent write and job-enqueue capabilities.
type PlatformClient interface {
	ListGrantIntents(ctx context.Context) ([]apikit.GrantIntent, error)
	ClaimAccessIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.AccessIntentObserverLeaseResponse, error)
	RecordGrantIntentObservation(ctx context.Context, grantID string, req apikit.GrantIntentObservationRequest) (apikit.GrantObservation, error)
}

// ObservedGrantCR is the sanitized Kubernetes MCPAccessGrant metadata read by the observer.
type ObservedGrantCR struct {
	UID               string
	Generation        int64
	GrantID           string
	ServerUID         string
	DesiredRevisionID string
	SpecDigest        string
	Disabled          bool
}

// GrantReader inspects live MCPAccessGrant CR state in Kubernetes.
type GrantReader interface {
	GetGrantCR(ctx context.Context, namespace, name string) (*ObservedGrantCR, bool, error)
}

// GatewayStatusProber queries every serving gateway replica for a target server UID.
type GatewayStatusProber interface {
	ProbeServingReplicas(ctx context.Context, namespace, serverName, serverUID string) ([]apikit.GrantGatewayReplicaEvidence, error)
}

// Config configures the independent access-intent observer.
type Config struct {
	ClusterKey string
	ObserverID string
	Interval   time.Duration
}

// Observer watches grant CRs and serving gateway replicas independently of workers.
type Observer struct {
	platform PlatformClient
	grants   GrantReader
	prober   GatewayStatusProber
	cfg      Config

	mu    sync.Mutex
	epoch int64
}

// New constructs an independent access-intent observer.
func New(platform PlatformClient, grants GrantReader, prober GatewayStatusProber, cfg Config) *Observer {
	if strings.TrimSpace(cfg.ClusterKey) == "" {
		cfg.ClusterKey = "default"
	}
	if strings.TrimSpace(cfg.ObserverID) == "" {
		cfg.ObserverID = "runtime-access-observer"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	return &Observer{
		platform: platform,
		grants:   grants,
		prober:   prober,
		cfg:      cfg,
	}
}

// Epoch returns the observer's currently held lease epoch (0 if unacquired).
func (o *Observer) Epoch() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.epoch
}

func (o *Observer) ensureLease(ctx context.Context) (int64, error) {
	o.mu.Lock()
	epoch := o.epoch
	o.mu.Unlock()
	if epoch > 0 {
		return epoch, nil
	}
	lease, err := o.platform.ClaimAccessIntentObserverLease(ctx, o.cfg.ClusterKey, o.cfg.ObserverID)
	if err != nil {
		return 0, err
	}
	o.mu.Lock()
	o.epoch = lease.ObserverEpoch
	o.mu.Unlock()
	return lease.ObserverEpoch, nil
}

func (o *Observer) resetLease() {
	o.mu.Lock()
	o.epoch = 0
	o.mu.Unlock()
}

// Run continuously polls grant CRs and serving gateway replicas until ctx is canceled.
func (o *Observer) Run(ctx context.Context) {
	ticker := time.NewTicker(o.cfg.Interval)
	defer ticker.Stop()
	for {
		_ = o.ReconcileOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce evaluates every durable grant intent once and writes fenced
// independent observations to Platform API.
func (o *Observer) ReconcileOnce(ctx context.Context) error {
	epoch, err := o.ensureLease(ctx)
	if err != nil {
		return err
	}
	intents, err := o.platform.ListGrantIntents(ctx)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		obsReq := o.evaluateGrant(ctx, intent, epoch)
		if _, err := o.platform.RecordGrantIntentObservation(ctx, intent.GrantID, obsReq); err != nil {
			if errors.Is(err, platformapi.ErrAccessIntentConflict) {
				o.resetLease()
				return fmt.Errorf("%w: grant %s", ErrStaleObserverEpoch, intent.GrantID)
			}
			return err
		}
	}
	return nil
}

func (o *Observer) evaluateGrant(ctx context.Context, intent apikit.GrantIntent, epoch int64) apikit.GrantIntentObservationRequest {
	now := time.Now().UTC()
	req := apikit.GrantIntentObservationRequest{
		ClusterKey:         o.cfg.ClusterKey,
		ObserverID:         o.cfg.ObserverID,
		ObserverEpoch:      epoch,
		ObservedRevisionID: intent.DesiredRevisionID,
		Health:             apikit.AccessIntentHealthUnknown,
		PropagationStatus:  apikit.GrantPropagationUnknown,
		VerifiedAt:         now,
	}

	cr, exists, crErr := o.grants.GetGrantCR(ctx, intent.Namespace, intent.Name)
	if crErr != nil {
		req.Health = apikit.AccessIntentHealthDegraded
		req.Reason = "GrantReadError"
		req.Message = crErr.Error()
		req.PropagationStatus = apikit.GrantPropagationUnknown
		return req
	}

	crAligned := false
	switch intent.State {
	case apikit.GrantIntentStateActive:
		if !exists {
			req.Health = apikit.AccessIntentHealthPending
			req.Reason = "GrantCRMissing"
			req.PropagationStatus = apikit.GrantPropagationPending
		} else {
			req.TargetUID = cr.UID
			if cr.GrantID != "" && cr.GrantID != intent.GrantID {
				req.Health = apikit.AccessIntentHealthDegraded
				req.Reason = "GrantOwnershipMismatch"
				req.PropagationStatus = apikit.GrantPropagationUnverified
				return req
			}
			if cr.DesiredRevisionID != "" && cr.DesiredRevisionID != intent.DesiredRevisionID {
				req.Health = apikit.AccessIntentHealthProgressing
				req.Reason = "GrantRevisionPending"
				req.ObservedRevisionID = cr.DesiredRevisionID
				req.PropagationStatus = apikit.GrantPropagationPending
			} else {
				crAligned = true
			}
		}
	case apikit.GrantIntentStateRevoked, apikit.GrantIntentStateDeleted:
		if exists && (cr.GrantID == "" || cr.GrantID == intent.GrantID) && !cr.Disabled {
			req.TargetUID = cr.UID
			req.Health = apikit.AccessIntentHealthProgressing
			req.Reason = "RevocationProjectionPending"
			req.PropagationStatus = apikit.GrantPropagationPending
		} else {
			crAligned = true
		}
	}

	replicas, probeErr := o.prober.ProbeServingReplicas(ctx, intent.Namespace, intent.ServerName, intent.ServerUID)
	if probeErr != nil {
		req.Health = apikit.AccessIntentHealthUnknown
		req.Reason = "GatewayProbeError"
		req.Message = probeErr.Error()
		req.PropagationStatus = apikit.GrantPropagationUnknown
		return req
	}

	req.ServingReplicas = len(replicas)
	req.GatewayEvidence = replicas
	if len(replicas) == 0 {
		req.Reason = "NoServingGatewayReplicas"
		req.PropagationStatus = apikit.GrantPropagationUnknown
		return req
	}

	expectedDigest := ""
	if intent.Receipt != nil {
		expectedDigest = intent.Receipt.SpecDigest
	}
	acked := 0
	unreachable := 0
	mismatched := 0
	for _, rep := range replicas {
		if !rep.Reachable {
			unreachable++
			continue
		}
		if !rep.Ready || rep.ServerUID != intent.ServerUID || rep.LastReloadError != "" {
			mismatched++
			continue
		}
		revMatches := rep.PolicyRevision == intent.DesiredRevisionID
		digestMatches := expectedDigest == "" || rep.PolicyDigest == "" || rep.PolicyDigest == expectedDigest
		if revMatches && digestMatches {
			acked++
		}
	}
	req.AcknowledgedReplicas = acked

	switch {
	case unreachable > 0:
		req.PropagationStatus = apikit.GrantPropagationUnknown
		req.Health = apikit.AccessIntentHealthDegraded
		req.Reason = "GatewayReplicaUnreachable"
	case !crAligned:
		req.PropagationStatus = apikit.GrantPropagationPending
	case mismatched > 0:
		req.PropagationStatus = apikit.GrantPropagationUnverified
		req.Health = apikit.AccessIntentHealthDegraded
		req.Reason = "GatewayReplicaMismatch"
	case acked < len(replicas):
		req.PropagationStatus = apikit.GrantPropagationPending
		req.Health = apikit.AccessIntentHealthProgressing
		req.Reason = "GatewayPropagationPending"
	default:
		req.PropagationStatus = apikit.GrantPropagationVerified
		if intent.State == apikit.GrantIntentStateDeleted {
			req.Health = apikit.AccessIntentHealthDeleted
			req.Reason = "GrantDeletedAndPropagated"
		} else if intent.State == apikit.GrantIntentStateRevoked {
			req.Health = apikit.AccessIntentHealthRevoked
			req.Reason = "GrantRevokedAndPropagated"
		} else {
			req.Health = apikit.AccessIntentHealthReady
			req.Reason = "GrantActiveAndPropagated"
		}
	}
	return req
}

// K8sGrantReader reads live MCPAccessGrant CRs via dynamic.Interface.
type K8sGrantReader struct {
	Dynamic dynamic.Interface
}

func (r *K8sGrantReader) GetGrantCR(ctx context.Context, namespace, name string) (*ObservedGrantCR, bool, error) {
	if r == nil || r.Dynamic == nil {
		return nil, false, errors.New("kubernetes dynamic client unavailable")
	}
	obj, err := r.Dynamic.Resource(grantGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	ann := obj.GetAnnotations()
	disabled := false
	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		if raw, ok := spec["disabled"].(bool); ok {
			disabled = raw
		}
	}
	return &ObservedGrantCR{
		UID:               string(obj.GetUID()),
		Generation:        obj.GetGeneration(),
		GrantID:           ann[accessintentworker.GrantIDAnnotation],
		ServerUID:         ann[mcpaccess.ServerUIDAnnotation],
		DesiredRevisionID: ann[accessintentworker.DesiredRevisionAnnotation],
		SpecDigest:        ann[accessintentworker.SpecDigestAnnotation],
		Disabled:          disabled,
	}, true, nil
}

// K8sGatewayStatusProber discovers serving gateway Pods for an MCPServer and
// queries each replica's /config/status endpoint.
type K8sGatewayStatusProber struct {
	Clientset  kubernetes.Interface
	HTTPClient *http.Client
	StatusPort int
}

type gatewayConfigStatusPayload struct {
	Ready                 bool     `json:"ready"`
	Revision              string   `json:"revision"`
	PolicyDigest          string   `json:"policy_digest"`
	ServerUID             string   `json:"server_uid"`
	LastReloadError       string   `json:"last_reload_error"`
	SessionSyncReady      bool     `json:"session_sync_ready"`
	SessionSyncAgeSeconds *float64 `json:"session_sync_age_seconds"`
}

func (p *K8sGatewayStatusProber) ProbeServingReplicas(ctx context.Context, namespace, serverName, serverUID string) ([]apikit.GrantGatewayReplicaEvidence, error) {
	if p == nil || p.Clientset == nil {
		return nil, errors.New("kubernetes clientset unavailable")
	}
	selector := fmt.Sprintf("app.kubernetes.io/instance=%s", serverName)
	pods, err := p.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	httpClient := p.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 3 * time.Second}
	}
	port := p.StatusPort
	if port <= 0 {
		port = 8080
	}
	out := make([]apikit.GrantGatewayReplicaEvidence, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.PodIP == "" {
			continue
		}
		ev := apikit.GrantGatewayReplicaEvidence{
			PodName:    pod.Name,
			ObservedAt: time.Now().UTC(),
		}
		statusURL := fmt.Sprintf("http://%s:%d/config/status", pod.Status.PodIP, port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			ev.Error = err.Error()
			out = append(out, ev)
			continue
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			ev.Error = err.Error()
			out = append(out, ev)
			continue
		}
		var payload gatewayConfigStatusPayload
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil {
			ev.Error = fmt.Sprintf("status %d", resp.StatusCode)
			out = append(out, ev)
			continue
		}
		ev.Reachable = true
		ev.Ready = payload.Ready
		ev.ServerUID = payload.ServerUID
		if ev.ServerUID == "" {
			ev.ServerUID = pod.Annotations[mcpaccess.ServerUIDAnnotation]
		}
		ev.PolicyRevision = payload.Revision
		ev.PolicyDigest = payload.PolicyDigest
		ev.LastReloadError = payload.LastReloadError
		ev.SessionSyncReady = payload.SessionSyncReady
		ev.SessionSyncAgeSeconds = payload.SessionSyncAgeSeconds
		out = append(out, ev)
	}
	return out, nil
}
