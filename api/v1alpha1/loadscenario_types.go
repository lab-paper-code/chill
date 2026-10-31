package v1alpha1

import (
	"fmt"
	"math"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// EvaluationObjectReference pins an immutable object in the release namespace
// (or a cluster-scoped Node). Only Workload references carry a generation.
type EvaluationObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Type=string
	UID types.UID `json:"uid"`
}

type EvaluationWorkloadReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Type=string
	UID types.UID `json:"uid"`
	// +kubebuilder:validation:Minimum=1
	Generation int64 `json:"generation"`
}

// EvaluationEvidenceReference identifies immutable evidence, never an endpoint.
type EvaluationEvidenceReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Store string `json:"store"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Key string `json:"key"`
	// +kubebuilder:validation:Minimum=1
	Size int64 `json:"size"`
}

// EvaluationNonNegative is a finite, nonnegative scientific input.
// +kubebuilder:validation:Minimum=0
// +kubebuilder:validation:Maximum=1.7976931348623157e308
type EvaluationNonNegative float64

// LoadScenarioPart fixes one interval; map keys are Workload names.
// +kubebuilder:validation:XValidation:rule="has(self.rates) != has(self.alpha)",message="exactly one of rates or alpha is required"
type LoadScenarioPart struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	DurationSeconds float64 `json:"durationSeconds"`
	// +optional
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=128
	Rates map[string]EvaluationNonNegative `json:"rates,omitempty"`
	// +optional
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=128
	Alpha map[string]EvaluationNonNegative `json:"alpha,omitempty"`
}

// LoadScenarioSpec is the sole source of workload identities and declared load.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="LoadScenario spec is immutable"
// +kubebuilder:validation:XValidation:rule="!self.parts.exists(p, has(p.alpha)) || (has(self.rateBasisRef) && has(self.kappaRef))",message="alpha requires rateBasisRef and kappaRef"
// +kubebuilder:validation:XValidation:rule="self.parts.all(p, (!has(p.rates) || p.rates.all(k, self.workloads.exists(w, w.name == k))) && (!has(p.alpha) || p.alpha.all(k, self.workloads.exists(w, w.name == k))))",message="load keys must name declared workloads"
// +kubebuilder:validation:XValidation:rule="self.arrivalDistribution.all(k, self.workloads.exists(w, w.name == k)) && self.workloads.all(w, w.name in self.arrivalDistribution)",message="arrivalDistribution must cover exactly the declared workloads"
// +kubebuilder:validation:XValidation:rule="self.parts.all(p, self.parts.filter(q, q.id == p.id).size() == 1)",message="part IDs must be unique"
type LoadScenarioSpec struct {
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Workloads []EvaluationWorkloadReference `json:"workloads"`
	// Parts is ordered; atomic list equality makes reordering an immutable change.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Parts []LoadScenarioPart `json:"parts"`
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=128
	ArrivalDistribution map[string]string `json:"arrivalDistribution"`
	// +optional
	// +kubebuilder:validation:MaxProperties=128
	TraceRefs          map[string]EvaluationEvidenceReference `json:"traceRefs,omitempty"`
	SeedRef            EvaluationEvidenceReference            `json:"seedRef"`
	PayloadManifestRef EvaluationEvidenceReference            `json:"payloadManifestRef"`
	// +optional
	RateBasisRef *EvaluationEvidenceReference `json:"rateBasisRef,omitempty"`
	// +optional
	KappaRef *EvaluationEvidenceReference `json:"kappaRef,omitempty"`
}

// Validate checks declarations once at the controller's creation boundary.
func (s *LoadScenarioSpec) Validate() error {
	if len(s.Workloads) == 0 || len(s.Parts) == 0 {
		return fmt.Errorf("workloads and parts are required")
	}
	names, uids := map[string]bool{}, map[types.UID]bool{}
	for _, w := range s.Workloads {
		if len(validation.IsDNS1123Subdomain(w.Name)) != 0 || w.UID == "" || w.Generation < 1 || names[w.Name] || uids[w.UID] {
			return fmt.Errorf("invalid or duplicate workload %q", w.Name)
		}
		names[w.Name], uids[w.UID] = true, true
		if s.ArrivalDistribution[w.Name] == "" {
			return fmt.Errorf("arrivalDistribution missing workload %q", w.Name)
		}
	}
	if len(s.ArrivalDistribution) != len(names) {
		return fmt.Errorf("arrivalDistribution must cover declared workloads")
	}
	for name, ref := range s.TraceRefs {
		if !names[name] || !validEvaluationEvidence(ref) {
			return fmt.Errorf("invalid trace reference %q", name)
		}
	}
	if !validEvaluationEvidence(s.SeedRef) || !validEvaluationEvidence(s.PayloadManifestRef) {
		return fmt.Errorf("seedRef and payloadManifestRef are required")
	}
	for _, ref := range []*EvaluationEvidenceReference{s.RateBasisRef, s.KappaRef} {
		if ref != nil && !validEvaluationEvidence(*ref) {
			return fmt.Errorf("invalid alpha evidence reference")
		}
	}
	ids := map[string]bool{}
	for _, p := range s.Parts {
		if len(validation.IsDNS1123Label(p.ID)) != 0 || ids[p.ID] || !finitePositive(p.DurationSeconds) {
			return fmt.Errorf("invalid or duplicate part %q", p.ID)
		}
		ids[p.ID] = true
		if (len(p.Rates) == 0) == (len(p.Alpha) == 0) {
			return fmt.Errorf("part %q requires rates or alpha exclusively", p.ID)
		}
		values := p.Rates
		if len(p.Alpha) > 0 {
			if s.RateBasisRef == nil || s.KappaRef == nil {
				return fmt.Errorf("alpha requires rateBasisRef and kappaRef")
			}
			values = p.Alpha
		}
		for name, value := range values {
			if !names[name] || !finiteNonNegative(float64(value)) {
				return fmt.Errorf("invalid load for %q in part %q", name, p.ID)
			}
		}
	}
	return nil
}

func finiteNonNegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func finitePositive(v float64) bool    { return finiteNonNegative(v) && v > 0 }
func validEvaluationEvidence(r EvaluationEvidenceReference) bool {
	return r.Store != "" && r.Key != "" && r.Size > 0
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=loadscenario
// +kubebuilder:printcolumn:name="Payload",type=string,JSONPath=`.spec.payloadManifestRef.key`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LoadScenario is reusable immutable load, without execution status.
type LoadScenario struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LoadScenarioSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type LoadScenarioList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LoadScenario `json:"items"`
}

func init() { SchemeBuilder.Register(&LoadScenario{}, &LoadScenarioList{}) }
