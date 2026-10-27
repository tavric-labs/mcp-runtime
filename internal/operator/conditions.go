package operator

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ConditionType represents the type of status condition.
type ConditionType string

const (
	// DeploymentReady indicates the deployment is ready.
	DeploymentReady ConditionType = "DeploymentReady"
	// ServiceReady indicates the service is ready.
	ServiceReady ConditionType = "ServiceReady"
	// IngressReady indicates the ingress is ready.
	IngressReady ConditionType = "IngressReady"
	// GatewayReady indicates the gateway configuration and sidecar are ready.
	GatewayReady ConditionType = "GatewayReady"
	// PolicyReady indicates policy data for the gateway has been generated.
	PolicyReady ConditionType = "PolicyReady"
	// CanaryReady indicates the canary deployment, when configured, is ready.
	CanaryReady ConditionType = "CanaryReady"
)

// ResourceReadiness tracks the readiness of different resource types.
type ResourceReadiness struct {
	Deployment bool
	Service    bool
	Ingress    bool
	Gateway    bool
	Policy     bool
	Canary     bool
}

// SetCondition sets a condition in the conditions slice.
// It updates an existing condition if present, or appends a new one.
// LastTransitionTime is only updated when the condition's Status actually flips.
func SetCondition(conditions *[]metav1.Condition, condType ConditionType, ready bool, reason, message string, generation int64) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}

	newCond := metav1.Condition{
		Type:               string(condType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: generation,
	}

	// Find existing condition
	existingIdx := -1
	for i, c := range *conditions {
		if c.Type == string(condType) {
			existingIdx = i
			break
		}
	}

	if existingIdx >= 0 {
		// Preserve LastTransitionTime if Status hasn't changed
		existingCond := (*conditions)[existingIdx]
		if existingCond.Status == newCond.Status {
			newCond.LastTransitionTime = existingCond.LastTransitionTime
		}
		(*conditions)[existingIdx] = newCond
	} else {
		*conditions = append(*conditions, newCond)
	}
}
