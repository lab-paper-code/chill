package profilederivation

import "time"

type Verdict string

const (
	VerdictAccepted     Verdict = "Accepted"
	VerdictRejected     Verdict = "Rejected"
	VerdictInsufficient Verdict = "Insufficient"
)

type Reason struct {
	Kind    Verdict `json:"kind"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
}

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
	Samples                     []Sample  `json:"samples"`
	Failures                    int       `json:"failures"`
	SourceTimestampAbsent       bool      `json:"sourceTimestampAbsent"`
	ConcurrentCHILLWorkloads    []string  `json:"concurrentCHILLWorkloads"`
	ExternalWorkloadChanges     []string  `json:"externalWorkloadChanges"`
	WorkloadWindowKnown         bool      `json:"workloadWindowKnown"`
	ThermalStateKnown           bool      `json:"thermalStateKnown"`
	ThermallyThrottled          bool      `json:"thermallyThrottled"`
	SteadyWindowVerified        bool      `json:"steadyWindowVerified"`
}
type AdmissionPolicy struct {
	Version                        string        `json:"version"`
	MinimumSamples                 int           `json:"minimumSamples"`
	MaximumGap                     time.Duration `json:"-"`
	MaximumGapSeconds              float64       `json:"maximumGapSeconds"`
	MaximumBoundaryDistance        time.Duration `json:"-"`
	MaximumBoundaryDistanceSeconds float64       `json:"maximumBoundaryDistanceSeconds"`
	MinimumCoverage                float64       `json:"minimumCoverage"`
	AllowReceiptTimestamps         bool          `json:"allowReceiptTimestamps"`
	RejectConcurrentCHILLWorkloads bool          `json:"rejectConcurrentCHILLWorkloads"`
	RejectExternalWorkloadChanges  bool          `json:"rejectExternalWorkloadChanges"`
}
type AdmissionResult struct {
	Verdict                     Verdict  `json:"verdict"`
	PolicyVersion               string   `json:"policyVersion"`
	StateIdentity               string   `json:"stateIdentity"`
	EnvironmentIdentity         string   `json:"environmentIdentity"`
	MeasurementProtocolIdentity string   `json:"measurementProtocolIdentity"`
	TrialID                     string   `json:"trialID"`
	Reasons                     []Reason `json:"reasons"`
	TotalWallJoules             *float64 `json:"totalWallJoules,omitempty"`
	WindowSeconds               float64  `json:"windowSeconds"`
	CompletedItems              int      `json:"completedItems"`
}

type AdmittedTrial struct {
	StateIdentity               string `json:"stateIdentity"`
	EnvironmentIdentity         string `json:"environmentIdentity"`
	MeasurementProtocolIdentity string `json:"measurementProtocolIdentity"`
	TrialID                     string `json:"trialID"`
	AdmissionPolicyVersion      string `json:"admissionPolicyVersion"`
	EstimatorIdentity           string `json:"estimatorIdentity"`
	// Baseline sufficient statistics are copied from the admitted exact-state
	// baseline for point aggregation. Curve retains the baseline object that
	// owns cross-point covariance.
	BaselineMeanWallWattsVariance float64 `json:"baselineMeanWallWattsVariance"`
	BaselineDegreesOfFreedom      int     `json:"baselineDegreesOfFreedom"`
	BaselineConfidenceLevel       float64 `json:"baselineConfidenceLevel"`
	ActiveWindowWallJoulesPerItem float64 `json:"activeWindowWallJoulesPerItem"`
	IdleBaselineSecondsPerItem    float64 `json:"idleBaselineSecondsPerItem"`
	Batch                         int     `json:"batch"`
	IncrementalWorkJoulesPerItem  float64 `json:"incrementalWorkJoulesPerItem"`
	LatencyP99Milliseconds        float64 `json:"latencyP99Milliseconds"`
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
	ModelLoadedBeforeSettling    bool                `json:"modelLoadedBeforeSettling"`
	SettlingWindowVerified       bool                `json:"settlingWindowVerified"`
	InferenceCountAfterModelLoad int                 `json:"inferenceCountAfterModelLoad"`
	ModelResidentAtEnd           bool                `json:"modelResidentAtEnd"`
	Samples                      []Sample            `json:"samples"`
	Failures                     int                 `json:"failures"`
	SourceTimestampAbsent        bool                `json:"sourceTimestampAbsent"`
	ConcurrentCHILLWorkloads     []string            `json:"concurrentCHILLWorkloads"`
	ExternalWorkloadChanges      []string            `json:"externalWorkloadChanges"`
	WorkloadWindowKnown          bool                `json:"workloadWindowKnown"`
	ThermalStateKnown            bool                `json:"thermalStateKnown"`
	ThermallyThrottled           bool                `json:"thermallyThrottled"`
}
type IdleAdmissionResult struct {
	Verdict                      Verdict             `json:"verdict"`
	PolicyVersion                string              `json:"policyVersion"`
	StateIdentity                string              `json:"stateIdentity"`
	EnvironmentIdentity          string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string              `json:"measurementProtocolIdentity"`
	TrialID                      string              `json:"trialID"`
	RawEvidence                  RawEvidenceIdentity `json:"rawEvidence"`
	Reasons                      []Reason            `json:"reasons"`
	TotalWallJoules              *float64            `json:"totalWallJoules,omitempty"`
	ModelLoadedIdleMeanWallWatts *float64            `json:"modelLoadedIdleMeanWallWatts,omitempty"`
	WindowSeconds                float64             `json:"windowSeconds"`
}
type IdleTrial struct {
	StateIdentity                string              `json:"stateIdentity"`
	EnvironmentIdentity          string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string              `json:"measurementProtocolIdentity"`
	TrialID                      string              `json:"trialID"`
	RawEvidence                  RawEvidenceIdentity `json:"rawEvidence"`
	AdmissionPolicyVersion       string              `json:"admissionPolicyVersion"`
	ModelLoadedIdleMeanWallWatts float64             `json:"modelLoadedIdleMeanWallWatts"`
}
type IdleBaseline struct {
	StateIdentity                string      `json:"stateIdentity"`
	EnvironmentIdentity          string      `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string      `json:"measurementProtocolIdentity"`
	Trials                       []IdleTrial `json:"trials"`
	ModelLoadedIdleMeanWallWatts float64     `json:"modelLoadedIdleMeanWallWatts"`
	// MeanWallWattsVariance is the sample variance divided by the idle trial count.
	MeanWallWattsVariance  float64 `json:"meanWallWattsVariance"`
	DegreesOfFreedom       int     `json:"degreesOfFreedom"`
	ConfidenceHalfWidth    float64 `json:"confidenceHalfWidth"`
	ConfidenceLevel        float64 `json:"confidenceLevel"`
	PolicyVersion          string  `json:"policyVersion"`
	AdmissionPolicyVersion string  `json:"admissionPolicyVersion"`
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
	StateIdentity                    string          `json:"stateIdentity"`
	EnvironmentIdentity              string          `json:"environmentIdentity"`
	MeasurementProtocolIdentity      string          `json:"measurementProtocolIdentity"`
	Batch                            int             `json:"batch"`
	PointPolicyVersion               string          `json:"pointPolicyVersion"`
	EstimatorIdentity                string          `json:"estimatorIdentity"`
	Trials                           []AdmittedTrial `json:"trials"`
	MeanIncrementalWorkJoulesPerItem float64         `json:"meanIncrementalWorkJoulesPerItem"`
	// IncrementalWorkSampleVariance has units of squared joules per squared item.
	IncrementalWorkSampleVariance  float64         `json:"incrementalWorkSampleVariance"`
	MeanIdleBaselineSecondsPerItem float64         `json:"meanIdleBaselineSecondsPerItem"`
	Confidence                     PointConfidence `json:"confidence"`
	LatencyP99Milliseconds         float64         `json:"latencyP99Milliseconds"`
}
