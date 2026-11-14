package v1alpha1

import (
	"fmt"
	"math"
	"regexp"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="BatchEnergyOptimization spec is immutable"
type BatchEnergyOptimizationSpec struct {
	State          MeasurementStateSpec `json:"state"`
	ActiveProtocol ActiveProtocolSpec   `json:"activeProtocol"`
	IdleProtocol   IdleProtocolSpec     `json:"idleProtocol"`

	// TrialsPerBatch is the number of completed active and idle observations used
	// to compare each batch under the minimum-energy objective.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:Maximum=3
	TrialsPerBatch int32 `json:"trialsPerBatch"`

	// +optional
	// +kubebuilder:default={}
	PointPolicy BatchEnergyOptimizationPointPolicySpec `json:"pointPolicy,omitempty"`

	// RetryLimit bounds retries at the same batch after retryable infrastructure
	// failures. Runtime OOM, execution failures and invalid evidence do not retry.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	RetryLimit int32 `json:"retryLimit"`

	// Session holds the settings of a ChillServer measurement session.
	// +optional
	Session *BatchEnergyOptimizationSessionSpec `json:"session,omitempty"`
}

// BatchEnergyOptimizationSessionSpec holds the measurement request's session
// settings. Durations are seconds unless the name says otherwise.
// +kubebuilder:validation:XValidation:rule="self.levelDurationLimitSeconds > self.meterLagSeconds + self.minimumPowerWindowSeconds",message="levelDurationLimitSeconds must exceed meterLagSeconds plus minimumPowerWindowSeconds"
// +kubebuilder:validation:XValidation:rule="self.requestLifetimeSeconds * 1000 > self.deliveryMarginMilliseconds",message="requestLifetimeSeconds must exceed deliveryMarginMilliseconds"
type BatchEnergyOptimizationSessionSpec struct {
	// MeterLagSeconds is the power meter lag discarded at each level start.
	// +kubebuilder:validation:Minimum=0
	MeterLagSeconds float64 `json:"meterLagSeconds"`
	// MinimumPowerWindowSeconds is the shortest power observation of a level.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	MinimumPowerWindowSeconds float64 `json:"minimumPowerWindowSeconds"`
	// MidLevelRatio is the mid level arrival rate as a ratio of saturation throughput.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMaximum=true
	// +kubebuilder:validation:Maximum=1
	MidLevelRatio float64 `json:"midLevelRatio"`
	// SparseMeanGapSeconds is the sparse level's mean idle gap.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	SparseMeanGapSeconds float64 `json:"sparseMeanGapSeconds"`
	// LevelDurationLimitSeconds is the longest predicted duration of one level.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	LevelDurationLimitSeconds float64 `json:"levelDurationLimitSeconds"`
	// PostLoadWindowSeconds observes the loaded actor without inference after the last level.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	PostLoadWindowSeconds float64 `json:"postLoadWindowSeconds"`
	// ConfidenceLevel, a decimal in [0.5, 1), is the level of every statistical
	// decision the derivation makes.
	// +kubebuilder:validation:Pattern=`^0\.[5-9][0-9]*$`
	ConfidenceLevel string `json:"confidenceLevel"`
	// +kubebuilder:validation:Minimum=1
	RequestLifetimeSeconds int32 `json:"requestLifetimeSeconds"`
	// +kubebuilder:validation:Minimum=1
	DeliveryMarginMilliseconds int32 `json:"deliveryMarginMilliseconds"`
	// +kubebuilder:validation:Minimum=1
	FramedDecodedRequestUpperBytes int64 `json:"framedDecodedRequestUpperBytes"`
	// Observation is how the session's power, prepare and remove operations
	// are measured, as in a stand-alone operation-cost measurement.
	Observation MeasurementObservationSpec `json:"observation"`
}

var sessionConfidenceDecimal = regexp.MustCompile(`^0\.[5-9][0-9]*$`)

// Confidence returns ConfidenceLevel as a number in [0.5, 1).
func (s BatchEnergyOptimizationSessionSpec) Confidence() (float64, error) {
	confidence, err := strconv.ParseFloat(s.ConfidenceLevel, 64)
	if err != nil || !sessionConfidenceDecimal.MatchString(s.ConfidenceLevel) {
		return 0, fmt.Errorf("session.confidenceLevel must be a decimal in [0.5, 1), got %q", s.ConfidenceLevel)
	}
	return confidence, nil
}

// BatchEnergyOptimizationPointPolicySpec owns the confidence of both idle and
// incremental-energy estimates for this measurement.
type BatchEnergyOptimizationPointPolicySpec struct {
	// +optional
	// +kubebuilder:default="0.95"
	// +kubebuilder:validation:Pattern=`^0\.[0-9]*[1-9][0-9]*$`
	ConfidenceLevel string `json:"confidenceLevel,omitempty"`
}

var pointConfidenceDecimal = regexp.MustCompile(`^0\.[0-9]*[1-9][0-9]*$`)

func (p BatchEnergyOptimizationPointPolicySpec) Confidence() (float64, error) {
	value := p.ConfidenceLevel
	if value == "" {
		value = "0.95"
	}
	confidence, err := strconv.ParseFloat(value, 64)
	if err != nil || !pointConfidenceDecimal.MatchString(value) ||
		math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence <= 0 || confidence >= 1 {
		return 0, fmt.Errorf("pointPolicy.confidenceLevel must be a decimal in (0, 1)")
	}
	return confidence, nil
}

// +kubebuilder:validation:XValidation:rule="!has(self.durationToleranceSeconds) || self.durationToleranceSeconds < double(self.durationSeconds)",message="duration tolerance must be less than duration"
// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type ActiveProtocolSpec struct {
	// Workload supplies the actual SLO percentile and latency for this sweep.
	// +kubebuilder:validation:MinLength=1
	Workload string `json:"workload"`

	// +kubebuilder:validation:Minimum=0
	WarmupIterations int32 `json:"warmupIterations"`

	// +kubebuilder:validation:Minimum=30
	DurationSeconds int32 `json:"durationSeconds"`

	// +optional
	// +kubebuilder:default=0.5
	// +kubebuilder:validation:Minimum=0
	DurationToleranceSeconds *float64 `json:"durationToleranceSeconds,omitempty"`

	// Dataset names the cluster-scoped Dataset whose profiling records every
	// active child Run samples.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Dataset string `json:"dataset"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
	OperatingPoints   MeasurementOperatingPointsSpec    `json:"operatingPoints"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.durationToleranceSeconds) || self.durationToleranceSeconds < double(self.durationSeconds)",message="duration tolerance must be less than duration"
// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type IdleProtocolSpec struct {

	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds int32 `json:"settlingDurationSeconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// +optional
	// +kubebuilder:default=0.5
	// +kubebuilder:validation:Minimum=0
	DurationToleranceSeconds *float64 `json:"durationToleranceSeconds,omitempty"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

type BatchEnergyOptimizationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Measuring;Complete;Failed
	Phase string `json:"phase,omitempty"`

	// +optional
	CompletedTrials int32 `json:"completedTrials,omitempty"`

	// CompletedBatches counts the batch sizes a session measured.
	// +optional
	CompletedBatches int32 `json:"completedBatches,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	Outcome string `json:"outcome,omitempty"`

	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// Terminal reports whether the measurement ended, Complete or Failed, for
// generation.
func (s BatchEnergyOptimizationStatus) Terminal(generation int64) bool {
	return s.ObservedGeneration == generation &&
		(s.Phase == string(MeasurementPhaseComplete) || s.Phase == string(MeasurementPhaseFailed))
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=beo
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trials",type=integer,JSONPath=`.status.completedTrials`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BatchEnergyOptimization searches one batch-size space for the minimum
// incremental energy per completed item by owning the ProfilingRun objects it
// creates.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 200",message="name must be at most 200 characters so generated Run names fit"
type BatchEnergyOptimization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BatchEnergyOptimizationSpec   `json:"spec"`
	Status BatchEnergyOptimizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type BatchEnergyOptimizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BatchEnergyOptimization `json:"items"`
}

func init() { SchemeBuilder.Register(&BatchEnergyOptimization{}, &BatchEnergyOptimizationList{}) }
