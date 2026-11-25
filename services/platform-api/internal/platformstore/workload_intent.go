package platformstore

import (
	"context"

	"mcp-platform-api/internal/workloadintent"
	"mcp-runtime/pkg/apikit"
)

// ClaimWorkloadObserverLease increments the monotonic workload status observer epoch.
func (s *Store) ClaimWorkloadObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadObserverLeaseResponse{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ClaimObserverLease(ctx, clusterKey, observerID)
}

func (s *Store) ListServerIntents(ctx context.Context) ([]apikit.ServerIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return nil, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ListServerIntents(ctx)
}

func (s *Store) GetServerIntent(ctx context.Context, serverID string) (apikit.ServerIntent, bool, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ServerIntent{}, false, workloadintent.ErrDisabled
	}
	return s.workloadIntents.GetServerIntent(ctx, serverID)
}

func (s *Store) UpsertServerIntent(ctx context.Context, req apikit.ServerIntentUpsertRequest) (apikit.ServerIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.UpsertServerIntent(ctx, req)
}

func (s *Store) RetireServerIntent(ctx context.Context, serverID string, req apikit.ServerIntentRetireRequest) (apikit.ServerIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RetireServerIntent(ctx, serverID, req)
}

func (s *Store) ClaimServerIntentFence(ctx context.Context, serverID, workerID string) (apikit.ServerIntentFenceResponse, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ServerIntentFenceResponse{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ClaimServerFence(ctx, serverID, workerID)
}

func (s *Store) RecordServerRetirementCheckpoint(ctx context.Context, serverID string, req apikit.ServerRetirementCheckpointRequest) (apikit.ServerIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ServerIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordServerRetirementCheckpoint(ctx, serverID, req)
}

func (s *Store) RecordServerIntentReceipt(ctx context.Context, serverID string, req apikit.ServerIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadExecutionReceipt{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordServerReceipt(ctx, serverID, req)
}

func (s *Store) RecordServerIntentObservation(ctx context.Context, serverID string, req apikit.ServerIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadStatusObservation{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordServerObservation(ctx, serverID, req)
}

func (s *Store) ListDeploymentIntents(ctx context.Context) ([]apikit.DeploymentIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return nil, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ListDeploymentIntents(ctx)
}

func (s *Store) GetDeploymentIntent(ctx context.Context, deploymentID string) (apikit.DeploymentIntent, bool, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.DeploymentIntent{}, false, workloadintent.ErrDisabled
	}
	return s.workloadIntents.GetDeploymentIntent(ctx, deploymentID)
}

func (s *Store) UpsertDeploymentIntent(ctx context.Context, req apikit.DeploymentIntentUpsertRequest) (apikit.DeploymentIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.DeploymentIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.UpsertDeploymentIntent(ctx, req)
}

func (s *Store) DeleteDeploymentIntent(ctx context.Context, deploymentID string, req apikit.DeploymentIntentDeleteRequest) (apikit.DeploymentIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.DeploymentIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.DeleteDeploymentIntent(ctx, deploymentID, req)
}

func (s *Store) ClaimDeploymentIntentFence(ctx context.Context, deploymentID, workerID string) (apikit.DeploymentIntentFenceResponse, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.DeploymentIntentFenceResponse{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ClaimDeploymentFence(ctx, deploymentID, workerID)
}

func (s *Store) RecordDeploymentIntentReceipt(ctx context.Context, deploymentID string, req apikit.DeploymentIntentReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadExecutionReceipt{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordDeploymentReceipt(ctx, deploymentID, req)
}

func (s *Store) RecordDeploymentIntentObservation(ctx context.Context, deploymentID string, req apikit.DeploymentIntentObservationRequest) (apikit.WorkloadStatusObservation, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadStatusObservation{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordDeploymentObservation(ctx, deploymentID, req)
}

func (s *Store) ListImagePublicationIntents(ctx context.Context) ([]apikit.ImagePublicationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return nil, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ListImagePublicationIntents(ctx)
}

func (s *Store) GetImagePublicationIntent(ctx context.Context, publicationID string) (apikit.ImagePublicationIntent, bool, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ImagePublicationIntent{}, false, workloadintent.ErrDisabled
	}
	return s.workloadIntents.GetImagePublicationIntent(ctx, publicationID)
}

func (s *Store) BeginImagePublication(ctx context.Context, req apikit.ImagePublicationBeginRequest) (apikit.ImagePublicationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ImagePublicationIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.BeginImagePublication(ctx, req)
}

func (s *Store) FinalizeImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationFinalizeRequest) (apikit.ImagePublicationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ImagePublicationIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.FinalizeImagePublication(ctx, publicationID, req)
}

func (s *Store) AbortImagePublication(ctx context.Context, publicationID string, req apikit.ImagePublicationAbortRequest) (apikit.ImagePublicationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ImagePublicationIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.AbortImagePublication(ctx, publicationID, req)
}

func (s *Store) ClaimImagePublicationFence(ctx context.Context, publicationID, workerID string) (apikit.ImagePublicationFenceResponse, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.ImagePublicationFenceResponse{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ClaimImagePublicationFence(ctx, publicationID, workerID)
}

func (s *Store) RecordImagePublicationReceipt(ctx context.Context, publicationID string, req apikit.ImagePublicationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadExecutionReceipt{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordImagePublicationReceipt(ctx, publicationID, req)
}

func (s *Store) ListRestartOperationIntents(ctx context.Context) ([]apikit.RestartOperationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return nil, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ListRestartOperationIntents(ctx)
}

func (s *Store) GetRestartOperationIntent(ctx context.Context, operationID string) (apikit.RestartOperationIntent, bool, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.RestartOperationIntent{}, false, workloadintent.ErrDisabled
	}
	return s.workloadIntents.GetRestartOperationIntent(ctx, operationID)
}

func (s *Store) CreateRestartOperation(ctx context.Context, req apikit.RestartOperationCreateRequest) (apikit.RestartOperationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.RestartOperationIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.CreateRestartOperation(ctx, req)
}

func (s *Store) ClaimRestartOperationFence(ctx context.Context, operationID, workerID string) (apikit.RestartOperationFenceResponse, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.RestartOperationFenceResponse{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.ClaimRestartOperationFence(ctx, operationID, workerID)
}

func (s *Store) RecordRestartComponentCheckpoint(ctx context.Context, operationID string, req apikit.RestartComponentCheckpointRequest) (apikit.RestartOperationIntent, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.RestartOperationIntent{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordRestartComponentCheckpoint(ctx, operationID, req)
}

func (s *Store) RecordRestartOperationReceipt(ctx context.Context, operationID string, req apikit.RestartOperationReceiptRequest) (apikit.WorkloadExecutionReceipt, error) {
	if s == nil || s.workloadIntents == nil {
		return apikit.WorkloadExecutionReceipt{}, workloadintent.ErrDisabled
	}
	return s.workloadIntents.RecordRestartOperationReceipt(ctx, operationID, req)
}
