package v1alpha1

import (
	"math"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// WorkloadSpec declares only the desired logical model and request SLO.
type WorkloadSpec struct {
	ModelRef WorkloadModelReference `json:"modelRef"`
	SLO      WorkloadSLO            `json:"slo"`
}

type WorkloadModelReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`
}

type WorkloadSLO struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Maximum=100
	Percentile float64 `json:"percentile"`
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="targetLatency must be a positive duration"
	// +kubebuilder:validation:XValidation:rule="duration(self) <= duration('2562047h47m16.854775807s')",message="targetLatency must fit a Go duration"
	TargetLatency metav1.Duration `json:"targetLatency"`
}

type WorkloadAppliedModelReference struct {
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
	Generation int64     `json:"generation"`
}

type WorkloadAppliedDefinition struct {
	WorkloadGeneration int64                         `json:"workloadGeneration"`
	ModelRef           WorkloadAppliedModelReference `json:"modelRef"`
	SLO                WorkloadSLO                   `json:"slo"`
}

type WorkloadAdmission struct {
	Allowed    bool        `json:"allowed"`
	Reason     string      `json:"reason,omitempty"`
	ObservedAt metav1.Time `json:"observedAt"`
}

// WorkloadOperation describes one accepted management change, not a queue.
type WorkloadOperation struct {
	// +kubebuilder:validation:Enum=Update;Delete;ModelUpdate
	Kind string `json:"kind"`
	// +kubebuilder:validation:Enum=Stopping;Draining;Detaching;Applying;Cleaning;Completed;Failed
	Phase            string `json:"phase"`
	TargetGeneration int64  `json:"targetGeneration"`
	// Target model identity is present only for a shared ModelSpec update.
	// +optional
	TargetModelUID types.UID `json:"targetModelUID,omitempty"`
	// +optional
	TargetModelGeneration int64       `json:"targetModelGeneration,omitempty"`
	AcceptedAt            metav1.Time `json:"acceptedAt"`
	// ActorUsername and ActorUID are the TokenReview-verified original caller.
	// +optional
	ActorUsername string `json:"actorUsername,omitempty"`
	// +optional
	ActorUID string `json:"actorUID,omitempty"`
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Error string `json:"error,omitempty"`
}

type WorkloadStatus struct {
	// +optional
	AppliedDefinition *WorkloadAppliedDefinition `json:"appliedDefinition,omitempty"`
	Admission         WorkloadAdmission          `json:"admission"`
	// +optional
	Operation *WorkloadOperation `json:"operation,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Workload identifies a service by name and a registration by UID.
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkloadSpec `json:"spec"`
	// +optional
	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{})
}

// ValidateWorkload validates the HTTP write boundary before Kubernetes storage.
// Model existence, references and operation serialization belong to the owner.
func ValidateWorkload(name string, spec WorkloadSpec) error {
	var errs field.ErrorList
	for _, message := range validation.IsDNS1123Subdomain(name) {
		errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), name, message))
	}
	for _, message := range validation.IsDNS1123Subdomain(spec.ModelRef.Name) {
		errs = append(errs, field.Invalid(field.NewPath("spec", "modelRef", "name"), spec.ModelRef.Name, message))
	}
	if math.IsNaN(spec.SLO.Percentile) || math.IsInf(spec.SLO.Percentile, 0) || spec.SLO.Percentile <= 0 || spec.SLO.Percentile > 100 {
		errs = append(errs, field.Invalid(field.NewPath("spec", "slo", "percentile"), spec.SLO.Percentile, "must be finite and in (0,100]"))
	}
	if spec.SLO.TargetLatency.Duration <= 0 {
		errs = append(errs, field.Invalid(field.NewPath("spec", "slo", "targetLatency"), spec.SLO.TargetLatency.String(), "must be a positive duration"))
	}
	return errs.ToAggregate()
}
