// Package teamintentworker runs the durable River worker that reconciles
// Platform API team namespace intents against Kubernetes with fencing and
// incarnation ownership checks.
package teamintentworker

import (
	"context"
	"errors"
	"strings"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/httpx"
)

const (
	// QueueName is the River queue for team namespace reconciliation jobs.
	QueueName = "team_namespace"
	// QueueSchema is the PostgreSQL schema owned by the team intent River queue.
	QueueSchema = "team_intent_queue"
	// JobKind matches services/platform-api/internal/teamintent/writer.go.
	JobKind = "reconcile_team_namespace"
)

// ReconcileJobArgs matches the JSON job payload inserted by Platform API's
// transactional team intent writer.
type ReconcileJobArgs struct {
	TeamID      string `json:"teamId"`
	RevisionID  string `json:"revisionId"`
	RequestID   string `json:"requestId,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

// Kind returns the River job kind for team namespace reconciliation.
func (ReconcileJobArgs) Kind() string { return JobKind }

// PlatformClient is the subset of Platform API's internal team intent contract
// required by the worker.
type PlatformClient interface {
	ClaimTeamIntentFence(ctx context.Context, teamID, workerID string) (apikit.TeamIntentFenceResponse, error)
	GetTeamIntent(ctx context.Context, teamID string) (apikit.TeamNamespaceIntent, bool, error)
	RecordTeamIntentReceipt(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) (apikit.TeamNamespaceReceipt, error)
}

// NamespaceReconciler performs ownership-checked Kubernetes namespace
// provisioning and deleted-team Traefik watch cleanup.
type NamespaceReconciler interface {
	ReconcileActive(ctx context.Context, intent apikit.TeamNamespaceIntent) (namespaceUID string, err error)
	CleanupDeleted(ctx context.Context, intent apikit.TeamNamespaceIntent) error
}

// Worker processes ReconcileJobArgs from River with per-team fencing,
// pre/post incarnation checks, and bounded execution receipts.
type Worker struct {
	river.WorkerDefaults[ReconcileJobArgs]
	workerID   string
	platform   PlatformClient
	reconciler NamespaceReconciler

	// BeforeMutate and AfterMutate are optional hooks used by tests to inject
	// concurrent state transitions during reconciliation.
	BeforeMutate func(ctx context.Context, intent apikit.TeamNamespaceIntent) error
	AfterMutate  func(ctx context.Context, intent apikit.TeamNamespaceIntent) error
}

// NewWorker constructs a fenced team namespace reconciliation worker.
func NewWorker(workerID string, platform PlatformClient, reconciler NamespaceReconciler) *Worker {
	return &Worker{
		workerID:   strings.TrimSpace(workerID),
		platform:   platform,
		reconciler: reconciler,
	}
}

// Work executes one durable reconciliation attempt for a team incarnation.
func (w *Worker) Work(ctx context.Context, job *river.Job[ReconcileJobArgs]) error {
	if job == nil {
		return river.JobCancel(errors.New("nil river job"))
	}
	if reqID := strings.TrimSpace(job.Args.RequestID); reqID != "" {
		ctx = httpx.ContextWithRequestID(ctx, reqID)
	}

	ctx, span := startLinkedWorkerSpan(ctx, job)
	defer span.End()

	teamID := strings.TrimSpace(job.Args.TeamID)
	if teamID == "" {
		err := errors.New("teamId is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return river.JobCancel(err)
	}

	fence, err := w.platform.ClaimTeamIntentFence(ctx, teamID, w.workerID)
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, platformapi.ErrTeamNotFound) {
			span.SetStatus(codes.Ok, "team not found")
			return river.JobCancel(err)
		}
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	intent := fence.Intent
	epoch := fence.FenceEpoch
	span.SetAttributes(
		attribute.String("team.namespace", intent.Namespace),
		attribute.String("intent.desired_revision_id", intent.DesiredRevisionID),
		attribute.Bool("intent.deleted", intent.Deleted),
		attribute.Int64("intent.fence_epoch", epoch),
	)

	if isSupersededIntent(intent) {
		return w.recordReceiptAllowConflict(ctx, teamID, apikit.TeamIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        epoch,
			Outcome:           apikit.TeamIntentOutcomeSuperseded,
			Attempt:           job.Attempt,
			WorkerID:          w.workerID,
		})
	}

	if w.BeforeMutate != nil {
		if err := w.BeforeMutate(ctx, intent); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	if intent.Deleted {
		return w.reconcileDeleted(ctx, span, job, intent, epoch)
	}
	return w.reconcileActive(ctx, span, job, intent, epoch)
}

func (w *Worker) reconcileActive(ctx context.Context, span trace.Span, job *river.Job[ReconcileJobArgs], intent apikit.TeamNamespaceIntent, epoch int64) error {
	uid, err := w.reconciler.ReconcileActive(ctx, intent)
	if err != nil {
		return w.handleReconcileError(ctx, span, job, intent, epoch, err)
	}

	if w.AfterMutate != nil {
		if err := w.AfterMutate(ctx, intent); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	latest, found, err := w.platform.GetTeamIntent(ctx, intent.TeamID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if !found {
		return nil
	}
	if latest.Deleted && latest.ActiveOwnerTeamID == "" {
		// Team was deleted mid-provisioning and no newer incarnation has claimed
		// the namespace; roll back the Traefik watch without deleting the namespace.
		if rollbackErr := w.reconciler.CleanupDeleted(ctx, latest); rollbackErr != nil && !errors.Is(rollbackErr, k8sprovision.ErrNamespaceOwnershipConflict) {
			span.RecordError(rollbackErr)
			span.SetStatus(codes.Error, rollbackErr.Error())
			return rollbackErr
		}
		return nil
	}
	if latest.DesiredRevisionID != intent.DesiredRevisionID || latest.Deleted || latest.ActiveOwnerTeamID != intent.TeamID {
		return nil
	}

	_, err = w.platform.RecordTeamIntentReceipt(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.TeamIntentOutcomeApplied,
		NamespaceUID:      uid,
		Attempt:           job.Attempt,
		WorkerID:          w.workerID,
	})
	if errors.Is(err, platformapi.ErrTeamIntentConflict) {
		if after, ok, getErr := w.platform.GetTeamIntent(ctx, intent.TeamID); getErr == nil && ok && after.Deleted && after.ActiveOwnerTeamID == "" {
			_ = w.reconciler.CleanupDeleted(ctx, after)
		}
		return nil
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (w *Worker) reconcileDeleted(ctx context.Context, span trace.Span, job *river.Job[ReconcileJobArgs], intent apikit.TeamNamespaceIntent, epoch int64) error {
	if err := w.reconciler.CleanupDeleted(ctx, intent); err != nil {
		return w.handleReconcileError(ctx, span, job, intent, epoch, err)
	}

	if w.AfterMutate != nil {
		if err := w.AfterMutate(ctx, intent); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	latest, found, err := w.platform.GetTeamIntent(ctx, intent.TeamID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if !found || latest.DesiredRevisionID != intent.DesiredRevisionID {
		return nil
	}
	if isSupersededIntent(latest) {
		return w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        epoch,
			Outcome:           apikit.TeamIntentOutcomeSuperseded,
			Attempt:           job.Attempt,
			WorkerID:          w.workerID,
		})
	}
	if !latest.Deleted || latest.ActiveOwnerTeamID != "" {
		return nil
	}

	return w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.TeamIntentOutcomeCleaned,
		Attempt:           job.Attempt,
		WorkerID:          w.workerID,
	})
}

func (w *Worker) handleReconcileError(ctx context.Context, span trace.Span, job *river.Job[ReconcileJobArgs], intent apikit.TeamNamespaceIntent, epoch int64, reconcileErr error) error {
	span.RecordError(reconcileErr)

	if errors.Is(reconcileErr, k8sprovision.ErrNamespaceOwnershipConflict) {
		latest, found, getErr := w.platform.GetTeamIntent(ctx, intent.TeamID)
		if getErr == nil {
			if !found || latest.DesiredRevisionID != intent.DesiredRevisionID {
				return nil
			}
			if isSupersededIntent(latest) {
				_ = w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
					DesiredRevisionID: intent.DesiredRevisionID,
					FenceEpoch:        epoch,
					Outcome:           apikit.TeamIntentOutcomeSuperseded,
					Attempt:           job.Attempt,
					WorkerID:          w.workerID,
				})
				return nil
			}
		}

		_ = w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        epoch,
			Outcome:           apikit.TeamIntentOutcomeTerminal,
			Attempt:           job.Attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.TeamIntentErrorOwnershipConflict,
			LastError:         reconcileErr.Error(),
		})
		span.SetStatus(codes.Error, reconcileErr.Error())
		return river.JobCancel(reconcileErr)
	}

	if errors.Is(reconcileErr, ErrInvalidConfig) {
		_ = w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
			DesiredRevisionID: intent.DesiredRevisionID,
			FenceEpoch:        epoch,
			Outcome:           apikit.TeamIntentOutcomeTerminal,
			Attempt:           job.Attempt,
			WorkerID:          w.workerID,
			LastErrorClass:    apikit.TeamIntentErrorInvalidConfig,
			LastError:         reconcileErr.Error(),
		})
		span.SetStatus(codes.Error, reconcileErr.Error())
		return river.JobCancel(reconcileErr)
	}

	errClass := apikit.TeamIntentErrorTransient
	if job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		errClass = apikit.TeamIntentErrorExhausted
	}
	_ = w.recordReceiptAllowConflict(ctx, intent.TeamID, apikit.TeamIntentReceiptRequest{
		DesiredRevisionID: intent.DesiredRevisionID,
		FenceEpoch:        epoch,
		Outcome:           apikit.TeamIntentOutcomeFailed,
		Attempt:           job.Attempt,
		WorkerID:          w.workerID,
		LastErrorClass:    errClass,
		LastError:         reconcileErr.Error(),
	})
	span.SetStatus(codes.Error, reconcileErr.Error())
	return reconcileErr
}

func (w *Worker) recordReceiptAllowConflict(ctx context.Context, teamID string, req apikit.TeamIntentReceiptRequest) error {
	_, err := w.platform.RecordTeamIntentReceipt(ctx, teamID, req)
	if errors.Is(err, platformapi.ErrTeamIntentConflict) {
		return nil
	}
	return err
}

func isSupersededIntent(intent apikit.TeamNamespaceIntent) bool {
	return strings.TrimSpace(intent.ActiveOwnerTeamID) != "" &&
		strings.TrimSpace(intent.ActiveOwnerTeamID) != strings.TrimSpace(intent.TeamID)
}

func startLinkedWorkerSpan(ctx context.Context, job *river.Job[ReconcileJobArgs]) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("team.id", job.Args.TeamID),
			attribute.String("intent.job_revision_id", job.Args.RevisionID),
			attribute.Int64("river.job_id", job.ID),
			attribute.Int("river.attempt", job.Attempt),
			attribute.String("request.id", job.Args.RequestID),
		),
	}
	if tp := strings.TrimSpace(job.Args.Traceparent); tp != "" {
		carrier := propagation.MapCarrier{"traceparent": tp}
		if ts := strings.TrimSpace(job.Args.Tracestate); ts != "" {
			carrier["tracestate"] = ts
		}
		linkedCtx := propagation.TraceContext{}.Extract(context.Background(), carrier)
		if link := trace.LinkFromContext(linkedCtx); link.SpanContext.IsValid() {
			opts = append(opts, trace.WithLinks(link))
		}
	}
	return otel.Tracer("mcp-runtime-api/teamintentworker").Start(ctx, "teamintent.reconcile", opts...)
}
