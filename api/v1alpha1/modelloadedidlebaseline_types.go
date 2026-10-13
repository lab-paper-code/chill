package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ModelLoadedIdleBaselinePhaseMeasuring    = "Measuring"
	ModelLoadedIdleBaselinePhaseComplete     = "Complete"
	ModelLoadedIdleBaselinePhaseInsufficient = "Insufficient"
)

// ModelLoadedIdleBaselinePointPolicySpec fixes the aggregate estimator inputs.
type ModelLoadedIdleBaselinePointPolicySpec struct {
	// Version identifies the complete point-estimation rule.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	Version string `json:"version"`

	// +kubebuilder:validation:Minimum=3
	MinimumRuns int32 `json:"minimumRuns"`

	// ConfidenceLevel is a decimal string in the interval (0, 1).
	// +kubebuilder:validation:Pattern=`^0\.[0-9]*[1-9][0-9]*$`
	ConfidenceLevel string `json:"confidenceLevel"`
}

// ModelLoadedIdleBaselineSpec requests one measured idle-power aggregate.
// +kubebuilder:validation:XValidation:rule="self.attemptLimit >= self.pointPolicy.minimumRuns",message="attemptLimit must cover minimumRuns"
// +kubebuilder:validation:XValidation:rule="quantity(self.cpuQuota).compareTo(quantity('0')) > 0",message="cpuQuota must be positive"
// +kubebuilder:validation:XValidation:rule="quantity(self.cpuQuota).isInteger()",message="cpuQuota must be a whole number of CPU cores"
// +kubebuilder:validation:XValidation:rule="quantity(self.state.cpuRequest).compareTo(quantity(self.cpuQuota)) <= 0",message="state.cpuRequest must not exceed cpuQuota"
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ModelLoadedIdleBaseline spec is immutable"
type ModelLoadedIdleBaselineSpec struct {
	State MeasurementStateSpec `json:"state"`

	// CPUQuota fixes the whole-core runtime quota for each owned idle Run.
	CPUQuota resource.Quantity `json:"cpuQuota"`

	// CPUAffinity selects explicit firstfloor CPU IDs for every owned idle Run.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=0
	// +listType=set
	CPUAffinity []int32 `json:"cpuAffinity,omitempty"`

	// +kubebuilder:validation:Minimum=1
	Batch int32 `json:"batch"`

	Protocol IdleProtocolSpec `json:"protocol"`

	PointPolicy ModelLoadedIdleBaselinePointPolicySpec `json:"pointPolicy"`

	// AttemptLimit bounds all terminal child Runs, including measured Runs.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:Maximum=100
	AttemptLimit int32 `json:"attemptLimit"`
}

// ModelLoadedIdleBaselineStatus is a compact projection of owned Runs and the
// retained aggregate result.
type ModelLoadedIdleBaselineStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Measuring;Complete;Insufficient
	Phase string `json:"phase,omitempty"`

	// +optional
	AttemptedRuns int32 `json:"attemptedRuns,omitempty"`

	// +optional
	CompletedRuns int32 `json:"completedRuns,omitempty"`

	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=idlebase
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Completed",type=integer,JSONPath=`.status.completedRuns`
// +kubebuilder:printcolumn:name="Attempts",type=integer,JSONPath=`.status.attemptedRuns`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 243",message="name must be at most 243 characters so the longest generated Run name fits"

// ModelLoadedIdleBaseline owns the idle-only ProfilingRuns contributing to one
// measured aggregate.
type ModelLoadedIdleBaseline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelLoadedIdleBaselineSpec   `json:"spec"`
	Status ModelLoadedIdleBaselineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ModelLoadedIdleBaselineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelLoadedIdleBaseline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelLoadedIdleBaseline{}, &ModelLoadedIdleBaselineList{})
}
