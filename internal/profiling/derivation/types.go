package profilederivation

import (
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type Sample struct {
	ObservedAt time.Time `json:"observedAt"`
	Watts      float64   `json:"watts"`
}
type Trial struct {
	StateIdentity               string    `json:"stateIdentity"`
	EnvironmentIdentity         string    `json:"environmentIdentity"`
	MeasurementProtocolIdentity string    `json:"measurementProtocolIdentity"`
	TrialID                     string    `json:"trialID"`
	Batch                       int       `json:"batch"`
	WindowStart                 time.Time `json:"windowStart"`
	WindowEnd                   time.Time `json:"windowEnd"`
	CompletedItems              int       `json:"completedItems"`
	CompletedCalls              int       `json:"completedCalls"`
	LatencyMilliseconds         []float64 `json:"latencyMilliseconds"`
	// NativeSamples retains aligned actual actor batch spans. Client latency is
	// kept above and is never reinterpreted as backend or runner service time.
	NativeSamples            []NativeServiceSample `json:"nativeSamples,omitempty"`
	Phases                   []OperatingPointPhase `json:"phases"`
	Samples                  []Sample              `json:"samples"`
	Failures                 int                   `json:"failures"`
	SourceTimestampAbsent    bool                  `json:"sourceTimestampAbsent"`
	ConcurrentCHILLWorkloads []string              `json:"concurrentCHILLWorkloads"`
	ExternalWorkloadChanges  []string              `json:"externalWorkloadChanges"`
	WorkloadWindowKnown      bool                  `json:"workloadWindowKnown"`
}

// NativeServiceSample copies one runtime batch execution unchanged. RecordIDs
// and Units are per request; units are analysis observations only.
type NativeServiceSample struct {
	BatchID    uint64   `json:"batchID"`
	BatchSize  int      `json:"batchSize"`
	RequestIDs []string `json:"requestIDs"`
	RecordIDs  []string `json:"recordIDs"`
	Units      []int    `json:"units"`
	firstfloor.ServiceSample
	RunnerServiceMilliseconds float64   `json:"runnerServiceMs"`
	PreprocessMilliseconds    float64   `json:"preprocessMs"`
	BackendCallMilliseconds   float64   `json:"backendCallMs"`
	PostprocessMilliseconds   float64   `json:"postprocessMs"`
	FirstTokenMilliseconds    float64   `json:"firstTokenMs,omitempty"`
	NativeRequestMilliseconds float64   `json:"nativeRequestMs"`
	StartedAt                 time.Time `json:"startedAt"`
	FinishedAt                time.Time `json:"finishedAt"`
	LatestEnqueuedAt          time.Time `json:"latestEnqueuedAt"`
	EarliestEnqueuedAt        time.Time `json:"earliestEnqueuedAt"`
}

func (s NativeServiceSample) Clone() NativeServiceSample {
	s.ServiceSample = s.ServiceSample.Clone()
	s.RequestIDs = slices.Clone(s.RequestIDs)
	s.RecordIDs, s.Units = slices.Clone(s.RecordIDs), slices.Clone(s.Units)
	return s
}

type EnergyTrial struct {
	StateIdentity               string `json:"stateIdentity"`
	EnvironmentIdentity         string `json:"environmentIdentity"`
	MeasurementProtocolIdentity string `json:"measurementProtocolIdentity"`
	TrialID                     string `json:"trialID"`
	EstimatorIdentity           string `json:"estimatorIdentity"`
	// Baseline sufficient statistics are copied from the completed exact-state
	// baseline for point aggregation. Curve retains the baseline object that
	// owns cross-point covariance.
	BaselineMeanWallWattsVariance  float64               `json:"baselineMeanWallWattsVariance"`
	BaselineDegreesOfFreedom       int                   `json:"baselineDegreesOfFreedom"`
	BaselineConfidenceLevel        float64               `json:"baselineConfidenceLevel"`
	ActiveWindowWallJoulesPerItem  float64               `json:"activeWindowWallJoulesPerItem"`
	IdleBaselineSecondsPerItem     float64               `json:"idleBaselineSecondsPerItem"`
	Batch                          int                   `json:"batch"`
	IncrementalWorkJoulesPerItem   float64               `json:"incrementalWorkJoulesPerItem"`
	LatencyP99Milliseconds         float64               `json:"latencyP99Milliseconds"`
	ClientLatencyMilliseconds      []float64             `json:"clientLatencyMilliseconds,omitempty"`
	NativeSamples                  []NativeServiceSample `json:"nativeSamples,omitempty"`
	PhasePower                     []PhasePower          `json:"phasePower"`
	DispatchOccupancyNanosPerInput []float64             `json:"dispatchOccupancyNanosPerInput"`
}

// OperatingPointPhase retains one measured load population. Only utilization
// phases enter energy-per-input; every phase contributes native service pairs.
type OperatingPointPhase struct {
	ID             string                `json:"id"`
	Kind           string                `json:"kind"`
	WindowStart    time.Time             `json:"windowStart"`
	WindowEnd      time.Time             `json:"windowEnd"`
	CompletedItems int                   `json:"completedItems"`
	Samples        []Sample              `json:"samples"`
	NativeSamples  []NativeServiceSample `json:"nativeSamples"`
}

type PhasePower struct {
	TrialID        string    `json:"trialID"`
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	WindowStart    time.Time `json:"windowStart"`
	WindowEnd      time.Time `json:"windowEnd"`
	CompletedItems int       `json:"completedItems"`
	WallJoules     float64   `json:"wallJoules"`
	MeanWallWatts  float64   `json:"meanWallWatts"`
}
type RawEvidenceIdentity struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Size  int64  `json:"size"`
}
type IdleObservation struct {
	StateIdentity                string              `json:"stateIdentity"`
	EnvironmentIdentity          string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string              `json:"measurementProtocolIdentity"`
	TrialID                      string              `json:"trialID"`
	RawEvidence                  RawEvidenceIdentity `json:"rawEvidence"`
	WindowStart                  time.Time           `json:"windowStart"`
	WindowEnd                    time.Time           `json:"windowEnd"`
	InferenceCountAfterModelLoad int                 `json:"inferenceCountAfterModelLoad"`
	ModelResidentAtEnd           bool                `json:"modelResidentAtEnd"`
	Samples                      []Sample            `json:"samples"`
	Failures                     int                 `json:"failures"`
	SourceTimestampAbsent        bool                `json:"sourceTimestampAbsent"`
	ConcurrentCHILLWorkloads     []string            `json:"concurrentCHILLWorkloads"`
	ExternalWorkloadChanges      []string            `json:"externalWorkloadChanges"`
	WorkloadWindowKnown          bool                `json:"workloadWindowKnown"`
}
type IdleTrial struct {
	StateIdentity                string              `json:"stateIdentity"`
	EnvironmentIdentity          string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string              `json:"measurementProtocolIdentity"`
	TrialID                      string              `json:"trialID"`
	RawEvidence                  RawEvidenceIdentity `json:"rawEvidence"`
	ModelLoadedIdleMeanWallWatts float64             `json:"modelLoadedIdleMeanWallWatts"`
}
type IdleBaseline struct {
	StateIdentity                string      `json:"stateIdentity"`
	EnvironmentIdentity          string      `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string      `json:"measurementProtocolIdentity"`
	Trials                       []IdleTrial `json:"trials"`
	ModelLoadedIdleMeanWallWatts float64     `json:"modelLoadedIdleMeanWallWatts"`
	// MeanWallWattsVariance is the sample variance divided by the idle trial count.
	MeanWallWattsVariance float64 `json:"meanWallWattsVariance"`
	DegreesOfFreedom      int     `json:"degreesOfFreedom"`
	ConfidenceHalfWidth   float64 `json:"confidenceHalfWidth"`
	ConfidenceLevel       float64 `json:"confidenceLevel"`
	PolicyVersion         string  `json:"policyVersion"`
}

// PointPolicy selects a versioned minimum-trial and confidence rule. The
// estimator method remains a package constant because only one is supported.
type PointPolicy struct {
	Version         string  `json:"version"`
	MinimumTrials   int     `json:"minimumTrials"`
	ConfidenceLevel float64 `json:"confidenceLevel"`
}

// PointConfidence versions the interval construction around the shared point
// estimate. A zero-variance Welch estimate retains its zero variance but has
// no effective degrees of freedom.
type PointConfidence struct {
	EstimatorVersion          string   `json:"estimatorVersion"`
	Method                    string   `json:"method"`
	Level                     float64  `json:"level"`
	VarianceOfMean            *float64 `json:"varianceOfMean,omitempty"`
	EffectiveDegreesOfFreedom *float64 `json:"effectiveDegreesOfFreedom,omitempty"`
	HalfWidth                 float64  `json:"halfWidth"`
}

type Point struct {
	StateIdentity                    string        `json:"stateIdentity"`
	EnvironmentIdentity              string        `json:"environmentIdentity"`
	MeasurementProtocolIdentity      string        `json:"measurementProtocolIdentity"`
	Batch                            int           `json:"batch"`
	PointPolicyVersion               string        `json:"pointPolicyVersion"`
	EstimatorIdentity                string        `json:"estimatorIdentity"`
	Trials                           []EnergyTrial `json:"trials"`
	MeanIncrementalWorkJoulesPerItem float64       `json:"meanIncrementalWorkJoulesPerItem"`
	// IncrementalWorkSampleVariance has units of squared joules per squared item.
	IncrementalWorkSampleVariance  float64         `json:"incrementalWorkSampleVariance"`
	MeanIdleBaselineSecondsPerItem float64         `json:"meanIdleBaselineSecondsPerItem"`
	Confidence                     PointConfidence `json:"confidence"`
	LatencyP99Milliseconds         float64         `json:"latencyP99Milliseconds"`
}
