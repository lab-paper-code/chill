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
	StateIdentity                    string  `json:"stateIdentity"`
	EnvironmentIdentity              string  `json:"environmentIdentity"`
	MeasurementProtocolIdentity      string  `json:"measurementProtocolIdentity"`
	TrialID                          string  `json:"trialID"`
	AdmissionPolicyVersion           string  `json:"admissionPolicyVersion"`
	EstimatorIdentity                string  `json:"estimatorIdentity"`
	BaselineUncertaintyJoulesPerItem float64 `json:"baselineUncertaintyJoulesPerItem"`
	// Baseline sufficient statistics are copied from the admitted exact-state
	// baseline for point aggregation. Curve retains the baseline object that
	// owns cross-point covariance.
	BaselineMeanWallWattsVariance     float64 `json:"baselineMeanWallWattsVariance"`
	BaselineDegreesOfFreedom          int     `json:"baselineDegreesOfFreedom"`
	ActiveWallJoulesPerItem           float64 `json:"activeWallJoulesPerItem"`
	BaselineCoefficientSecondsPerItem float64 `json:"baselineCoefficientSecondsPerItem"`
	Batch                             int     `json:"batch"`
	IncrementalJoulesPerItem          float64 `json:"incrementalJoulesPerItem"`
	LatencyP99Milliseconds            float64 `json:"latencyP99Milliseconds"`
}
type RawEvidenceIdentity struct {
	Store  string `json:"store"`
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
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
	Verdict                     Verdict             `json:"verdict"`
	PolicyVersion               string              `json:"policyVersion"`
	StateIdentity               string              `json:"stateIdentity"`
	EnvironmentIdentity         string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity string              `json:"measurementProtocolIdentity"`
	TrialID                     string              `json:"trialID"`
	RawEvidence                 RawEvidenceIdentity `json:"rawEvidence"`
	Reasons                     []Reason            `json:"reasons"`
	TotalWallJoules             *float64            `json:"totalWallJoules,omitempty"`
	MeanWallWatts               *float64            `json:"meanWallWatts,omitempty"`
	WindowSeconds               float64             `json:"windowSeconds"`
}
type IdleTrial struct {
	StateIdentity               string              `json:"stateIdentity"`
	EnvironmentIdentity         string              `json:"environmentIdentity"`
	MeasurementProtocolIdentity string              `json:"measurementProtocolIdentity"`
	TrialID                     string              `json:"trialID"`
	RawEvidence                 RawEvidenceIdentity `json:"rawEvidence"`
	AdmissionPolicyVersion      string              `json:"admissionPolicyVersion"`
	MeanWallWatts               float64             `json:"meanWallWatts"`
}
type IdleBaseline struct {
	StateIdentity               string      `json:"stateIdentity"`
	EnvironmentIdentity         string      `json:"environmentIdentity"`
	MeasurementProtocolIdentity string      `json:"measurementProtocolIdentity"`
	Trials                      []IdleTrial `json:"trials"`
	MeanWallWatts               float64     `json:"meanWallWatts"`
	// MeanWallWattsVariance is the sample variance divided by the idle trial count.
	MeanWallWattsVariance  float64 `json:"meanWallWattsVariance"`
	DegreesOfFreedom       int     `json:"degreesOfFreedom"`
	ConfidenceHalfWidth    float64 `json:"confidenceHalfWidth"`
	PolicyVersion          string  `json:"policyVersion"`
	AdmissionPolicyVersion string  `json:"admissionPolicyVersion"`
}

// PointPolicy selects a versioned minimum-trial rule. The first scientific
// path fixes the estimator itself at PointConfidenceMethod and
// PointConfidenceLevel instead of exposing method-shaped fields that cannot
// vary.
type PointPolicy struct {
	Version       string `json:"version"`
	MinimumTrials int    `json:"minimumTrials"`
}

// PointConfidence versions one interval construction around the shared point
// estimate. The retained pre-Welch construction has neither a variance nor
// effective degrees of freedom. A zero-variance Welch estimate retains its
// zero variance but has no effective degrees of freedom.
type PointConfidence struct {
	EstimatorVersion          string   `json:"estimatorVersion"`
	Method                    string   `json:"method"`
	Level                     float64  `json:"level"`
	VarianceOfMean            *float64 `json:"varianceOfMean,omitempty"`
	EffectiveDegreesOfFreedom *float64 `json:"effectiveDegreesOfFreedom,omitempty"`
	HalfWidth                 float64  `json:"halfWidth"`
}

type Point struct {
	StateIdentity                string          `json:"stateIdentity"`
	EnvironmentIdentity          string          `json:"environmentIdentity"`
	MeasurementProtocolIdentity  string          `json:"measurementProtocolIdentity"`
	Batch                        int             `json:"batch"`
	PointPolicyVersion           string          `json:"pointPolicyVersion"`
	EstimatorIdentity            string          `json:"estimatorIdentity"`
	Trials                       []AdmittedTrial `json:"trials"`
	MeanIncrementalJoulesPerItem float64         `json:"meanIncrementalJoulesPerItem"`
	// IncrementalSampleVariance has units of squared joules per squared item.
	IncrementalSampleVariance             float64         `json:"incrementalSampleVariance"`
	MeanBaselineCoefficientSecondsPerItem float64         `json:"meanBaselineCoefficientSecondsPerItem"`
	Confidence                            PointConfidence `json:"confidence"`
	PreviousConfidence                    PointConfidence `json:"previousConfidence"`
	LatencyP99Milliseconds                float64         `json:"latencyP99Milliseconds"`
}

type BSatStatus string

const (
	BSatAccepted      BSatStatus = "Accepted"
	BSatCandidateOnly BSatStatus = "CandidateOnly"
	BSatCensored      BSatStatus = "Censored"
	BSatAmbiguous     BSatStatus = "Ambiguous"
	BSatUnavailable   BSatStatus = "Unavailable"

	BSatCodeDiscoveryCandidateSelected     = "DiscoveryCandidateSelected"
	BSatCodeCapabilityBoundary             = "CapabilityBoundary"
	BSatCodeNoSaturationObserved           = "NoSaturationObserved"
	BSatCodeConfirmatoryTrialsSized        = "ConfirmatoryTrialsSized"
	BSatCodeConfirmatoryBudgetInsufficient = "ConfirmatoryBudgetInsufficient"
)

type LegacyBSatPolicy struct {
	Version                      string  `json:"version"`
	RelativeEquivalenceTolerance float64 `json:"relativeEquivalenceTolerance"`
}
type LegacyBSatResult struct {
	Status                      BSatStatus `json:"status"`
	PolicyVersion               string     `json:"policyVersion"`
	StateIdentity               string     `json:"stateIdentity,omitempty"`
	EnvironmentIdentity         string     `json:"environmentIdentity,omitempty"`
	MeasurementProtocolIdentity string     `json:"measurementProtocolIdentity,omitempty"`
	Batch                       *int       `json:"batch,omitempty"`
	Code                        string     `json:"code"`
	MeasuredBatches             []int      `json:"measuredBatches"`
}

// BSatPolicy is fixed before discovery starts. The complete decoded-policy
// provenance is added at the policy-file boundary; this pure value owns the
// fields needed by discovery and confirmation.
type BSatPolicy struct {
	Version                      string  `json:"version"`
	KMax                         int     `json:"kmax"`
	RelativeImprovementTolerance float64 `json:"relativeImprovementTolerance"`
	DiscoveryTrials              int     `json:"discoveryTrials"`
	MaximumConfirmatoryTrials    int     `json:"maximumConfirmatoryTrials"`
	MaximumActiveRunBudget       int     `json:"maximumActiveRunBudget"`
}

type BSatDiscoveryResult struct {
	Status                               BSatStatus `json:"status"`
	Code                                 string     `json:"code"`
	CandidateBatch                       *int       `json:"candidateBatch,omitempty"`
	CandidateMaximumRemainingImprovement *float64   `json:"candidateMaximumRemainingImprovement,omitempty"`
	MeasuredBatches                      []int      `json:"measuredBatches"`
	PolicyVersion                        string     `json:"policyVersion"`
	KMax                                 int        `json:"kmax"`
	RelativeImprovementTolerance         float64    `json:"relativeImprovementTolerance"`
	DiscoveryTrials                      int        `json:"discoveryTrials"`
	CurveFamilyContractDigest            string     `json:"curveFamilyContractDigest"`
	CurveFamilyIdentity                  string     `json:"curveFamilyIdentity"`
	BoundaryBatch                        *int       `json:"boundaryBatch,omitempty"`
}

type BSatSizingResult struct {
	Status                            BSatStatus `json:"status"`
	Code                              string     `json:"code"`
	CandidateBatch                    int        `json:"candidateBatch"`
	ConfirmatoryTrials                *int       `json:"confirmatoryTrials,omitempty"`
	ConfirmatoryBatches               []int      `json:"confirmatoryBatches"`
	MaximumFeasibleConfirmatoryTrials int        `json:"maximumFeasibleConfirmatoryTrials"`
	GlobalStatementCount              int        `json:"globalStatementCount"`
	OneSidedQuantile                  float64    `json:"oneSidedQuantile"`
	ProjectedReferenceLowerBound      *float64   `json:"projectedReferenceLowerBound,omitempty"`
	ProjectedMaximumRelativeHalfWidth *float64   `json:"projectedMaximumRelativeHalfWidth,omitempty"`
	ProjectedTotalActiveRuns          *int       `json:"projectedTotalActiveRuns,omitempty"`
	SizingMethod                      string     `json:"sizingMethod"`
	PolicyVersion                     string     `json:"policyVersion"`
	CurveFamilyContractDigest         string     `json:"curveFamilyContractDigest"`
	CurveFamilyIdentity               string     `json:"curveFamilyIdentity"`
}
