/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// HealthGatedRolloutSpec defines the desired state of HealthGatedRollout
type HealthGatedRolloutSpec struct {
	// stableDeployment is the trusted Deployment managed by this rollout.
	// +kubebuilder:validation:MinLength=1
	StableDeployment string `json:"stableDeployment"`

	// canaryDeployment is the candidate Deployment managed by this rollout.
	// +kubebuilder:validation:MinLength=1
	CanaryDeployment string `json:"canaryDeployment"`

	// rollbackStableReplicas is restored when the canary is unhealthy.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	RollbackStableReplicas int32 `json:"rollbackStableReplicas,omitempty"`

	// steps defines the stable-to-canary replica ratios used during promotion.
	// +kubebuilder:validation:MinItems=1
	Steps []RolloutStep `json:"steps"`

	// prometheusURL is the base URL of the Prometheus HTTP API.
	// +kubebuilder:validation:Pattern=`^https?://`
	PrometheusURL string `json:"prometheusURL"`

	// requestCountQuery must return the number of canary requests in the observation window.
	// +kubebuilder:validation:MinLength=1
	RequestCountQuery string `json:"requestCountQuery"`

	// errorRateQuery must return the canary HTTP error fraction from zero to one.
	// +kubebuilder:validation:MinLength=1
	ErrorRateQuery string `json:"errorRateQuery"`

	// latencyQuery must return canary latency in seconds.
	// +kubebuilder:validation:MinLength=1
	LatencyQuery string `json:"latencyQuery"`

	// minimumRequestCount prevents health decisions based on too little traffic.
	// +kubebuilder:validation:Minimum=1
	MinimumRequestCount int64 `json:"minimumRequestCount"`

	// maxErrorRate is the largest healthy error fraction.
	MaxErrorRate resource.Quantity `json:"maxErrorRate"`

	// maxLatencySeconds is the largest healthy latency value.
	MaxLatencySeconds resource.Quantity `json:"maxLatencySeconds"`

	// failureThreshold is the number of consecutive unhealthy evaluations required for rollback.
	// +kubebuilder:validation:Minimum=1
	FailureThreshold int32 `json:"failureThreshold"`

	// evaluationIntervalSeconds controls how often Prometheus is queried.
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:default=15
	EvaluationIntervalSeconds int32 `json:"evaluationIntervalSeconds,omitempty"`

	// cooldownSeconds controls how long a rolled-back rollout waits before retrying.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=300
	CooldownSeconds int32 `json:"cooldownSeconds,omitempty"`
}

// RolloutStep defines one approximate traffic split through replica counts.
type RolloutStep struct {
	// +kubebuilder:validation:Minimum=0
	StableReplicas int32 `json:"stableReplicas"`

	// +kubebuilder:validation:Minimum=1
	CanaryReplicas int32 `json:"canaryReplicas"`
}

// HealthGatedRolloutStatus defines the observed state of HealthGatedRollout.
type HealthGatedRolloutStatus struct {
	// observedGeneration is the spec generation used for the latest decision.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes the rollout state.
	Phase string `json:"phase,omitempty"`

	// currentStep is the zero-based index in spec.steps.
	CurrentStep int32 `json:"currentStep,omitempty"`

	// consecutiveFailures is reset after each healthy evaluation.
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// lastEvaluationTime records the most recent Prometheus decision.
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// rollbackTime starts the cooldown period after a rollback.
	RollbackTime *metav1.Time `json:"rollbackTime,omitempty"`

	// conditions represent the current state of the HealthGatedRollout resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// HealthGatedRollout is the Schema for the healthgatedrollouts API
type HealthGatedRollout struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of HealthGatedRollout
	// +required
	Spec HealthGatedRolloutSpec `json:"spec"`

	// status defines the observed state of HealthGatedRollout
	// +optional
	Status HealthGatedRolloutStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HealthGatedRolloutList contains a list of HealthGatedRollout
type HealthGatedRolloutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HealthGatedRollout `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &HealthGatedRollout{}, &HealthGatedRolloutList{})
		return nil
	})
}
