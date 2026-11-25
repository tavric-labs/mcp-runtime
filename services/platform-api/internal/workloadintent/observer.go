package workloadintent

import (
	"context"
	"fmt"
	"strings"

	"mcp-runtime/pkg/apikit"
)

// ClaimObserverLease atomically increments the monotonic observer epoch for
// clusterKey so older observers cannot overwrite fresher observations.
func (w *Writer) ClaimObserverLease(ctx context.Context, clusterKey, observerID string) (apikit.WorkloadObserverLeaseResponse, error) {
	if w == nil || w.db == nil {
		return apikit.WorkloadObserverLeaseResponse{}, ErrDisabled
	}
	clusterKey = strings.TrimSpace(clusterKey)
	if clusterKey == "" {
		clusterKey = defaultClusterKey
	}
	observerID = strings.TrimSpace(observerID)
	if observerID == "" {
		return apikit.WorkloadObserverLeaseResponse{}, fmt.Errorf("%w: observer_id is required", ErrInvalidInput)
	}
	var epoch int64
	err := w.db.QueryRowContext(ctx, `
INSERT INTO workload_intent.observer_lease (cluster_key, observer_id, observer_epoch, updated_at)
VALUES ($1, $2, 1, clock_timestamp())
ON CONFLICT (cluster_key) DO UPDATE SET
  observer_id = EXCLUDED.observer_id,
  observer_epoch = workload_intent.observer_lease.observer_epoch + 1,
  updated_at = EXCLUDED.updated_at
RETURNING observer_epoch`, clusterKey, observerID).Scan(&epoch)
	if err != nil {
		return apikit.WorkloadObserverLeaseResponse{}, err
	}
	return apikit.WorkloadObserverLeaseResponse{
		ClusterKey:    clusterKey,
		ObserverID:    observerID,
		ObserverEpoch: epoch,
	}, nil
}
