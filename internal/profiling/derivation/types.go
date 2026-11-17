package profilederivation

import (
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type Sample struct {
	ObservedAt time.Time `json:"observedAt"`
	Watts      float64   `json:"watts"`
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

// PointPolicy selects a versioned minimum-trial and confidence rule.
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
