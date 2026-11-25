// Package teamintentobserver runs the independent Kubernetes status observer
// for durable team namespace intents. It inspects live cluster state and writes
// leased observations to Platform API without mutating intent, receipts, or
// River jobs.
package teamintentobserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/kubeworkload"
)

const (
	ReasonAllPrerequisitesReady      = "AllPrerequisitesReady"
	ReasonPrerequisiteDrifted        = "PrerequisiteDrifted"
	ReasonNamespaceMissing           = "NamespaceMissing"
	ReasonNamespaceTerminating       = "NamespaceTerminating"
	ReasonNamespaceOwnershipMismatch = "NamespaceOwnershipMismatch"
	ReasonNamespaceUIDMismatch       = "NamespaceUIDMismatch"
	ReasonAwaitingReconcile          = "AwaitingReconcile"
	ReasonReconciling                = "Reconciling"
	ReasonReconcileTerminal          = "ReconcileTerminal"
	ReasonReconcileExhausted         = "ReconcileExhausted"
	ReasonCleanupComplete            = "CleanupComplete"
	ReasonCleanupPending             = "CleanupPending"
	ReasonCleanupTerminal            = "CleanupTerminal"
	ReasonTraefikWatchDrifted        = "TraefikWatchDrifted"
	ReasonSupersededByNewIncarnation = "SupersededByNewIncarnation"
)

const (
	defaultClusterKey            = "default"
	defaultVerifyInterval        = 30 * time.Second
	defaultInformerResync        = 5 * time.Minute
	defaultQueueMaxDepth         = 1024
	defaultResourceQuotaName     = "platform-default-quota"
	defaultLimitRangeName        = "platform-default-limits"
	defaultDenyNetworkPolicyName = "platform-default-deny"
	genericWorkloadIngressNetPol = "platform-generic-workload-ingress"
	traefikIngressWatchArgPrefix = "--providers.kubernetesingress.namespaces="
	traefikCRDWatchArgPrefix     = "--providers.kubernetescrd.namespaces="
)

// ErrObserverLeaseLost is returned when Platform API rejects an observation
// because a newer observer holds a higher lease epoch.
var ErrObserverLeaseLost = errors.New("observer lease superseded by newer epoch")

// PlatformClient exposes only intent reads, observer lease claims, and
// observation writes. By design it cannot mutate intent, receipts, or jobs.
type PlatformClient interface {
	ListTeamIntents(ctx context.Context) ([]apikit.TeamNamespaceIntent, error)
	GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error)
	ClaimTeamIntentObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.TeamIntentObserverLeaseResponse, error)
	RecordTeamIntentObservation(ctx context.Context, teamID string, req apikit.TeamIntentObservationRequest) (apikit.TeamNamespaceObservation, error)
}

// Config configures the independent team namespace status observer.
type Config struct {
	ClusterKey     string
	ObserverID     string
	Platform       PlatformClient
	K8sClient      kubernetes.Interface
	Traefik        k8sprovision.TraefikWatchConfig
	VerifyInterval time.Duration
	InformerResync time.Duration
	QueueMaxDepth  int
	Now            func() time.Time
	Logger         *slog.Logger
}

// Observer watches managed Kubernetes namespaces and periodically verifies
// live prerequisites against Platform API's team namespace intents.
type Observer struct {
	clusterKey     string
	observerID     string
	platform       PlatformClient
	k8s            kubernetes.Interface
	traefik        k8sprovision.TraefikWatchConfig
	verifyInterval time.Duration
	informerResync time.Duration
	queue          *dedupeQueue
	now            func() time.Time
	logger         *slog.Logger

	mu    sync.RWMutex
	epoch int64
}

// New validates cfg and creates an Observer.
func New(cfg Config) (*Observer, error) {
	observerID := strings.TrimSpace(cfg.ObserverID)
	if observerID == "" {
		return nil, fmt.Errorf("observer ID is required")
	}
	if cfg.Platform == nil {
		return nil, fmt.Errorf("platform client is required")
	}
	if cfg.K8sClient == nil {
		return nil, fmt.Errorf("kubernetes client is required")
	}
	clusterKey := strings.TrimSpace(cfg.ClusterKey)
	if clusterKey == "" {
		clusterKey = defaultClusterKey
	}
	verifyInterval := cfg.VerifyInterval
	if verifyInterval <= 0 {
		verifyInterval = defaultVerifyInterval
	}
	informerResync := cfg.InformerResync
	if informerResync <= 0 {
		informerResync = defaultInformerResync
	}
	queueMaxDepth := cfg.QueueMaxDepth
	if queueMaxDepth <= 0 {
		queueMaxDepth = defaultQueueMaxDepth
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Observer{
		clusterKey:     clusterKey,
		observerID:     observerID,
		platform:       cfg.Platform,
		k8s:            cfg.K8sClient,
		traefik:        cfg.Traefik,
		verifyInterval: verifyInterval,
		informerResync: informerResync,
		queue:          newDedupeQueue(queueMaxDepth),
		now:            nowFn,
		logger:         logger,
	}, nil
}

// ClaimLease acquires a monotonic observer epoch from Platform API.
func (o *Observer) ClaimLease(ctx context.Context) error {
	lease, err := o.platform.ClaimTeamIntentObserverLease(ctx, o.clusterKey, o.observerID)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.epoch = lease.ObserverEpoch
	o.mu.Unlock()
	return nil
}

// Epoch returns the currently held observer lease epoch.
func (o *Observer) Epoch() int64 {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.epoch
}

// Run claims an observer lease (if not already held), starts a SharedInformer
// on managed namespaces, runs an initial full sweep, and continuously handles
// informer events and periodic freshness verification until ctx is cancelled
// or a newer observer supersedes the lease.
func (o *Observer) Run(ctx context.Context) error {
	if o.Epoch() <= 0 {
		if err := o.ClaimLease(ctx); err != nil {
			return err
		}
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		o.k8s,
		o.informerResync,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = kubeworkload.PlatformManagedLabel + "=true"
		}),
	)
	nsInformer := factory.Core().V1().Namespaces().Informer()
	_, err := nsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    o.handleNamespaceEvent,
		UpdateFunc: func(_, newObj any) { o.handleNamespaceEvent(newObj) },
		DeleteFunc: o.handleNamespaceEvent,
	})
	if err != nil {
		return fmt.Errorf("register managed namespace informer handler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), nsInformer.HasSynced) {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("wait for managed namespace informer cache sync")
	}

	if err := o.ObserveAll(ctx); err != nil {
		if errors.Is(err, ErrObserverLeaseLost) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		o.logger.Warn("initial team intent observation sweep failed", "error", err)
	}

	ticker := time.NewTicker(o.verifyInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := o.ObserveAll(ctx); err != nil {
				if errors.Is(err, ErrObserverLeaseLost) {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				o.logger.Warn("periodic team intent observation sweep failed", "error", err)
			}
		case <-o.queue.Notify():
			for {
				if ctx.Err() != nil {
					return nil
				}
				teamID, fullSweep, ok := o.queue.Dequeue()
				if !ok {
					break
				}
				var obsErr error
				if fullSweep {
					obsErr = o.ObserveAll(ctx)
				} else {
					obsErr = o.ObserveTeam(ctx, teamID)
				}
				if errors.Is(obsErr, ErrObserverLeaseLost) {
					return obsErr
				}
				if obsErr != nil && ctx.Err() == nil {
					o.logger.Warn("team intent observation failed", "team_id", teamID, "full_sweep", fullSweep, "error", obsErr)
				}
			}
		}
	}
}

func (o *Observer) handleNamespaceEvent(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	ns, ok := obj.(*corev1.Namespace)
	if !ok || ns == nil {
		o.queue.EnqueueFullSweep()
		return
	}
	if teamID := strings.TrimSpace(ns.Labels[k8sprovision.TeamIDLabel]); teamID != "" {
		o.queue.Enqueue(teamID)
		return
	}
	o.queue.EnqueueFullSweep()
}

// ObserveAll fetches all team intents from Platform API and records fresh
// live observations for every incarnation.
func (o *Observer) ObserveAll(ctx context.Context) error {
	intents, err := o.platform.ListTeamIntents(ctx)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := o.observeIntent(ctx, intent); err != nil {
			if errors.Is(err, ErrObserverLeaseLost) {
				return err
			}
			return err
		}
	}
	return nil
}

// ObserveTeam fetches one team intent and records its live Kubernetes observation.
func (o *Observer) ObserveTeam(ctx context.Context, teamID string) error {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil
	}
	intent, found, err := o.platform.GetTeamIntent(ctx, teamID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return o.observeIntent(ctx, intent)
}

func (o *Observer) observeIntent(ctx context.Context, intent apikit.TeamNamespaceIntent) error {
	epoch := o.Epoch()
	if epoch <= 0 {
		return fmt.Errorf("observer lease not claimed")
	}
	verifiedAt := o.now().UTC()
	req, err := o.evaluateIntent(ctx, intent, epoch, verifiedAt)
	if err != nil {
		return err
	}

	_, err = o.platform.RecordTeamIntentObservation(ctx, intent.TeamID, req)
	if errors.Is(err, platformapi.ErrTeamIntentConflict) {
		latest, found, getErr := o.platform.GetTeamIntent(ctx, intent.TeamID)
		if getErr == nil && found && latest.DesiredRevisionID != intent.DesiredRevisionID {
			// Intent revision advanced while we were inspecting Kubernetes;
			// enqueue a follow-up check rather than stepping down.
			o.queue.Enqueue(intent.TeamID)
			return nil
		}
		return ErrObserverLeaseLost
	}
	if errors.Is(err, platformapi.ErrTeamNotFound) {
		return nil
	}
	return err
}

func (o *Observer) evaluateIntent(ctx context.Context, intent apikit.TeamNamespaceIntent, epoch int64, verifiedAt time.Time) (apikit.TeamIntentObservationRequest, error) {
	base := apikit.TeamIntentObservationRequest{
		ClusterKey:    o.clusterKey,
		ObserverID:    o.observerID,
		ObserverEpoch: epoch,
		VerifiedAt:    verifiedAt,
	}

	activeOwner := strings.TrimSpace(intent.ActiveOwnerTeamID)
	teamID := strings.TrimSpace(intent.TeamID)

	// Any incarnation superseded by a different active team incarnation is Deleted/SupersededByNewIncarnation.
	if activeOwner != "" && activeOwner != teamID {
		base.ObservedRevisionID = intent.DesiredRevisionID
		base.Health = apikit.TeamIntentHealthDeleted
		base.Reason = ReasonSupersededByNewIncarnation
		base.Message = fmt.Sprintf("superseded by active team %s", activeOwner)
		base.Conditions = []apikit.TeamIntentCondition{{
			Type:             "IncarnationSuperseded",
			Status:           "True",
			Reason:           ReasonSupersededByNewIncarnation,
			Message:          base.Message,
			RevisionID:       intent.DesiredRevisionID,
			LastTransitionAt: verifiedAt,
		}}
		return base, nil
	}

	if intent.Deleted {
		return o.evaluateDeletedIntent(ctx, intent, base, verifiedAt)
	}
	return o.evaluateActiveIntent(ctx, intent, base, verifiedAt)
}

func (o *Observer) evaluateDeletedIntent(ctx context.Context, intent apikit.TeamNamespaceIntent, base apikit.TeamIntentObservationRequest, verifiedAt time.Time) (apikit.TeamIntentObservationRequest, error) {
	namespace := strings.TrimSpace(intent.Namespace)
	if namespace != "" {
		ns, err := o.k8s.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if err == nil {
			base.NamespaceUID = string(ns.UID)
		} else if !apierrors.IsNotFound(err) {
			return apikit.TeamIntentObservationRequest{}, err
		}
	}

	watched, err := o.traefikWatchesNamespace(ctx, namespace)
	if err != nil {
		return apikit.TeamIntentObservationRequest{}, err
	}
	if watched {
		if intent.Receipt != nil && intent.Receipt.Outcome == apikit.TeamIntentOutcomeTerminal {
			base.Health = apikit.TeamIntentHealthDegraded
			base.Reason = ReasonCleanupTerminal
			base.Message = intent.Receipt.LastError
		} else if intent.Receipt != nil && intent.Receipt.Outcome == apikit.TeamIntentOutcomeCleaned {
			base.Health = apikit.TeamIntentHealthDegraded
			base.Reason = ReasonTraefikWatchDrifted
			base.Message = fmt.Sprintf("traefik still watches deleted team namespace %q", namespace)
		} else {
			base.Health = apikit.TeamIntentHealthProgressing
			base.Reason = ReasonCleanupPending
			base.Message = fmt.Sprintf("awaiting traefik watch removal for namespace %q", namespace)
		}
		base.Conditions = []apikit.TeamIntentCondition{{
			Type:             "TraefikWatchRemoved",
			Status:           "False",
			Reason:           base.Reason,
			Message:          base.Message,
			RevisionID:       intent.DesiredRevisionID,
			LastTransitionAt: verifiedAt,
		}}
		return base, nil
	}

	base.ObservedRevisionID = intent.DesiredRevisionID
	base.Health = apikit.TeamIntentHealthDeleted
	base.Reason = ReasonCleanupComplete
	base.Message = fmt.Sprintf("traefik watch removed and namespace %q retained", namespace)
	base.Conditions = []apikit.TeamIntentCondition{{
		Type:             "TraefikWatchRemoved",
		Status:           "True",
		Reason:           ReasonCleanupComplete,
		Message:          base.Message,
		RevisionID:       intent.DesiredRevisionID,
		LastTransitionAt: verifiedAt,
	}}
	return base, nil
}

func (o *Observer) evaluateActiveIntent(ctx context.Context, intent apikit.TeamNamespaceIntent, base apikit.TeamIntentObservationRequest, verifiedAt time.Time) (apikit.TeamIntentObservationRequest, error) {
	if intent.Receipt == nil || (intent.Receipt.Outcome == "" && intent.Receipt.AppliedRevisionID == "") {
		base.Health = apikit.TeamIntentHealthPending
		base.Reason = ReasonAwaitingReconcile
		base.Message = "waiting for worker execution"
		return base, nil
	}
	if intent.Receipt.Outcome == apikit.TeamIntentOutcomeTerminal {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonReconcileTerminal
		base.Message = intent.Receipt.LastError
		return base, nil
	}
	if intent.Receipt.Outcome == apikit.TeamIntentOutcomeFailed && intent.Receipt.LastErrorClass == apikit.TeamIntentErrorExhausted {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonReconcileExhausted
		base.Message = intent.Receipt.LastError
		return base, nil
	}
	if intent.Receipt.Outcome != apikit.TeamIntentOutcomeApplied || intent.Receipt.AppliedRevisionID == "" || intent.Receipt.AppliedRevisionID != intent.DesiredRevisionID {
		base.Health = apikit.TeamIntentHealthProgressing
		base.Reason = ReasonReconciling
		base.Message = "worker reconciliation in progress"
		return base, nil
	}

	namespace := strings.TrimSpace(intent.Namespace)
	ns, err := o.k8s.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		base.ObservedRevisionID = intent.Receipt.AppliedRevisionID
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonNamespaceMissing
		base.Message = fmt.Sprintf("namespace %q does not exist", namespace)
		return base, nil
	}
	if err != nil {
		return apikit.TeamIntentObservationRequest{}, err
	}

	base.NamespaceUID = string(ns.UID)
	base.ObservedRevisionID = intent.Receipt.AppliedRevisionID

	if ns.Status.Phase == corev1.NamespaceTerminating || ns.DeletionTimestamp != nil {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonNamespaceTerminating
		base.Message = fmt.Sprintf("namespace %q is terminating", namespace)
		return base, nil
	}
	if strings.TrimSpace(ns.Labels[kubeworkload.PlatformManagedLabel]) != "true" || strings.TrimSpace(ns.Labels[k8sprovision.TeamIDLabel]) != strings.TrimSpace(intent.TeamID) {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonNamespaceOwnershipMismatch
		base.Message = fmt.Sprintf("namespace %q ownership labels do not match team %s", namespace, intent.TeamID)
		return base, nil
	}
	if expectedUID := strings.TrimSpace(intent.Receipt.NamespaceUID); expectedUID != "" && string(ns.UID) != expectedUID {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonNamespaceUIDMismatch
		base.Message = fmt.Sprintf("namespace %q UID %q differs from applied receipt UID %q", namespace, ns.UID, expectedUID)
		base.Conditions = []apikit.TeamIntentCondition{{
			Type:             "NamespaceIdentityMatch",
			Status:           "False",
			Reason:           ReasonNamespaceUIDMismatch,
			Message:          base.Message,
			RevisionID:       intent.Receipt.AppliedRevisionID,
			LastTransitionAt: verifiedAt,
		}}
		return base, nil
	}

	missing, err := o.missingNamespacePrerequisites(ctx, namespace)
	if err != nil {
		return apikit.TeamIntentObservationRequest{}, err
	}
	if len(missing) > 0 {
		base.Health = apikit.TeamIntentHealthDegraded
		base.Reason = ReasonPrerequisiteDrifted
		base.Message = "missing namespace prerequisites: " + strings.Join(missing, ", ")
		base.Conditions = []apikit.TeamIntentCondition{{
			Type:             "PrerequisitesReady",
			Status:           "False",
			Reason:           ReasonPrerequisiteDrifted,
			Message:          base.Message,
			RevisionID:       intent.Receipt.AppliedRevisionID,
			LastTransitionAt: verifiedAt,
		}}
		return base, nil
	}

	base.Health = apikit.TeamIntentHealthReady
	base.Reason = ReasonAllPrerequisitesReady
	base.Message = fmt.Sprintf("namespace %q and prerequisites verified", namespace)
	base.Conditions = []apikit.TeamIntentCondition{
		{
			Type:             "NamespaceReady",
			Status:           "True",
			Reason:           ReasonAllPrerequisitesReady,
			RevisionID:       intent.Receipt.AppliedRevisionID,
			LastTransitionAt: verifiedAt,
		},
		{
			Type:             "PrerequisitesReady",
			Status:           "True",
			Reason:           ReasonAllPrerequisitesReady,
			RevisionID:       intent.Receipt.AppliedRevisionID,
			LastTransitionAt: verifiedAt,
		},
	}
	return base, nil
}

func (o *Observer) missingNamespacePrerequisites(ctx context.Context, namespace string) ([]string, error) {
	var missing []string

	check := func(name string, err error) error {
		if apierrors.IsNotFound(err) {
			missing = append(missing, name)
			return nil
		}
		return err
	}

	if _, err := o.k8s.CoreV1().ResourceQuotas(namespace).Get(ctx, defaultResourceQuotaName, metav1.GetOptions{}); err != nil {
		if err := check("resourcequota/"+defaultResourceQuotaName, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.CoreV1().LimitRanges(namespace).Get(ctx, defaultLimitRangeName, metav1.GetOptions{}); err != nil {
		if err := check("limitrange/"+defaultLimitRangeName, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.NetworkingV1().NetworkPolicies(namespace).Get(ctx, defaultDenyNetworkPolicyName, metav1.GetOptions{}); err != nil {
		if err := check("networkpolicy/"+defaultDenyNetworkPolicyName, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.NetworkingV1().NetworkPolicies(namespace).Get(ctx, genericWorkloadIngressNetPol, metav1.GetOptions{}); err != nil {
		if err := check("networkpolicy/"+genericWorkloadIngressNetPol, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.CoreV1().ServiceAccounts(namespace).Get(ctx, kubeworkload.DefaultServiceAccountName, metav1.GetOptions{}); err != nil {
		if err := check("serviceaccount/"+kubeworkload.DefaultServiceAccountName, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.RbacV1().RoleBindings(namespace).Get(ctx, k8sprovision.PlatformAPISecretAccessName, metav1.GetOptions{}); err != nil {
		if err := check("rolebinding/"+k8sprovision.PlatformAPISecretAccessName, err); err != nil {
			return nil, err
		}
	}
	if _, err := o.k8s.RbacV1().RoleBindings(namespace).Get(ctx, k8sprovision.OperatorSecretAccessName, metav1.GetOptions{}); err != nil {
		if err := check("rolebinding/"+k8sprovision.OperatorSecretAccessName, err); err != nil {
			return nil, err
		}
	}

	watchManaged, dep, err := o.traefikWatchDeployment(ctx)
	if err != nil {
		return nil, err
	}
	if watchManaged {
		if _, err := o.k8s.RbacV1().Roles(namespace).Get(ctx, k8sprovision.TraefikWatchRoleName, metav1.GetOptions{}); err != nil {
			if err := check("role/"+k8sprovision.TraefikWatchRoleName, err); err != nil {
				return nil, err
			}
		}
		if _, err := o.k8s.RbacV1().RoleBindings(namespace).Get(ctx, k8sprovision.TraefikWatchRoleName, metav1.GetOptions{}); err != nil {
			if err := check("rolebinding/"+k8sprovision.TraefikWatchRoleName, err); err != nil {
				return nil, err
			}
		}
		if dep == nil || !deploymentWatchesNamespace(dep, namespace) {
			missing = append(missing, "traefik-watch/"+namespace)
		}
	}

	return missing, nil
}

func (o *Observer) traefikWatchDeployment(ctx context.Context) (bool, *appsv1.Deployment, error) {
	if o.traefik.Mode == "disabled" || strings.TrimSpace(o.traefik.Deployment) == "" || strings.TrimSpace(o.traefik.Namespace) == "" {
		return false, nil, nil
	}
	if o.traefik.Mode == "auto" && strings.TrimSpace(o.traefik.Namespace) == "kube-system" {
		return false, nil, nil
	}
	dep, err := o.k8s.AppsV1().Deployments(o.traefik.Namespace).Get(ctx, o.traefik.Deployment, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if o.traefik.Mode == "required" {
			return true, nil, nil
		}
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if o.traefik.Mode == "auto" && !deploymentHasNamespaceWatchArg(dep) {
		return false, dep, nil
	}
	return true, dep, nil
}

func (o *Observer) traefikWatchesNamespace(ctx context.Context, namespace string) (bool, error) {
	watchManaged, dep, err := o.traefikWatchDeployment(ctx)
	if err != nil || !watchManaged || dep == nil {
		return false, err
	}
	return deploymentWatchesNamespace(dep, namespace), nil
}

func deploymentHasNamespaceWatchArg(dep *appsv1.Deployment) bool {
	if dep == nil {
		return false
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, arg := range c.Args {
			if strings.HasPrefix(arg, traefikIngressWatchArgPrefix) || strings.HasPrefix(arg, traefikCRDWatchArgPrefix) {
				return true
			}
		}
	}
	return false
}

func deploymentWatchesNamespace(dep *appsv1.Deployment, namespace string) bool {
	if dep == nil || strings.TrimSpace(namespace) == "" {
		return false
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, arg := range c.Args {
			for _, prefix := range []string{traefikIngressWatchArgPrefix, traefikCRDWatchArgPrefix} {
				if !strings.HasPrefix(arg, prefix) {
					continue
				}
				for _, entry := range strings.Split(strings.TrimPrefix(arg, prefix), ",") {
					if strings.TrimSpace(entry) == namespace {
						return true
					}
				}
			}
		}
	}
	return false
}

// dedupeQueue is a strictly bounded deduplicated FIFO queue that collapses
// overflow into a single full-sweep signal so event storms cannot grow memory
// without bound.
type dedupeQueue struct {
	mu         sync.Mutex
	maxDepth   int
	items      []string
	present    map[string]struct{}
	fullSweep  bool
	notifyChan chan struct{}
}

func newDedupeQueue(maxDepth int) *dedupeQueue {
	if maxDepth <= 0 {
		maxDepth = defaultQueueMaxDepth
	}
	return &dedupeQueue{
		maxDepth:   maxDepth,
		present:    make(map[string]struct{}),
		notifyChan: make(chan struct{}, 1),
	}
}

func (q *dedupeQueue) Enqueue(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		q.EnqueueFullSweep()
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fullSweep {
		q.signalLocked()
		return
	}
	if _, exists := q.present[teamID]; exists {
		return
	}
	if len(q.items) >= q.maxDepth {
		q.items = nil
		clear(q.present)
		q.fullSweep = true
		q.signalLocked()
		return
	}
	q.items = append(q.items, teamID)
	q.present[teamID] = struct{}{}
	q.signalLocked()
}

func (q *dedupeQueue) EnqueueFullSweep() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
	clear(q.present)
	q.fullSweep = true
	q.signalLocked()
}

func (q *dedupeQueue) Dequeue() (string, bool, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fullSweep {
		q.fullSweep = false
		return "", true, true
	}
	if len(q.items) == 0 {
		return "", false, false
	}
	next := q.items[0]
	q.items = q.items[1:]
	delete(q.present, next)
	return next, false, true
}

func (q *dedupeQueue) Notify() <-chan struct{} {
	return q.notifyChan
}

func (q *dedupeQueue) signalLocked() {
	select {
	case q.notifyChan <- struct{}{}:
	default:
	}
}
