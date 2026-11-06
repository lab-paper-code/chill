package v1alpha1

import (
	"fmt"
	"math"
	"regexp"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// EvaluationObjectReference pins an immutable object in the release namespace
// (or a cluster-scoped Node). Workload generation is optional provenance.
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
	// +optional
	Generation int64 `json:"generation,omitempty"`
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
type EvaluationNonNegative float64

// LoadScenarioPart fixes one interval; map keys are Workload names.
type LoadScenarioPart struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	DurationSeconds float64 `json:"durationSeconds"`
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=128
	Rates map[string]EvaluationNonNegative `json:"rates"`
}

// LoadScenarioSpec is the sole source of workload identities and declared load.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="LoadScenario spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.parts.all(p, p.rates.all(k, self.workloads.exists(w, w.name == k)))",message="load keys must name declared workloads"
// +kubebuilder:validation:XValidation:rule="self.seeds.all(k, self.workloads.exists(w, w.name == k)) && self.workloads.all(w, w.name in self.seeds)",message="seeds must cover exactly the declared workloads"
// +kubebuilder:validation:XValidation:rule="self.datasetRefs.all(k, self.workloads.exists(w, w.name == k)) && self.workloads.all(w, w.name in self.datasetRefs)",message="datasetRefs must cover exactly the declared workloads"
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
	TraceRefs map[string]EvaluationEvidenceReference `json:"traceRefs,omitempty"`
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=32
	Seeds map[string]EvaluationSeed `json:"seeds"`
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=32
	DatasetRefs map[string]EvaluationObjectReference `json:"datasetRefs"`
}

// EvaluationSeed preserves all uint64 bits through JSON clients.
// +kubebuilder:validation:Pattern=`^[0-9]+$`
// +kubebuilder:validation:MaxLength=20
// +kubebuilder:validation:XValidation:rule="self.size() < 20 || self <= '18446744073709551615'",message="seed must fit uint64"
type EvaluationSeed string

var evaluationSeedPattern = regexp.MustCompile(`^[0-9]+$`)

// Validate checks declarations once at the controller's creation boundary.
func (s *LoadScenarioSpec) Validate() error {
	if len(s.Workloads) == 0 || len(s.Parts) == 0 {
		return fmt.Errorf("workloads and parts are required")
	}
	names, uids := map[string]bool{}, map[types.UID]bool{}
	for _, w := range s.Workloads {
		if len(validation.IsDNS1123Subdomain(w.Name)) != 0 || w.UID == "" || names[w.Name] || uids[w.UID] {
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
	if len(s.Seeds) != len(names) || len(s.DatasetRefs) != len(names) {
		return fmt.Errorf("seeds and datasetRefs must cover exactly the declared workloads")
	}
	for name := range names {
		seed := string(s.Seeds[name])
		if len(seed) > 20 || !evaluationSeedPattern.MatchString(seed) {
			return fmt.Errorf("invalid seed for %q", name)
		}
		if _, err := strconv.ParseUint(seed, 10, 64); err != nil {
			return fmt.Errorf("invalid seed for %q: %w", name, err)
		}
		ref := s.DatasetRefs[name]
		if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.UID == "" {
			return fmt.Errorf("invalid dataset reference for %q", name)
		}
	}
	ids := map[string]bool{}
	for _, p := range s.Parts {
		if len(validation.IsDNS1123Label(p.ID)) != 0 || ids[p.ID] || !finitePositive(p.DurationSeconds) {
			return fmt.Errorf("invalid or duplicate part %q", p.ID)
		}
		ids[p.ID] = true
		if len(p.Rates) == 0 {
			return fmt.Errorf("part %q requires rates", p.ID)
		}
		for name, value := range p.Rates {
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
