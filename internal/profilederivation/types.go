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
	StateIdentity         string    `json:"stateIdentity"`
	TrialID               string    `json:"trialID"`
	Batch                 int       `json:"batch"`
	WindowStart           time.Time `json:"windowStart"`
	WindowEnd             time.Time `json:"windowEnd"`
	CompletedItems        int       `json:"completedItems"`
	CompletedCalls        int       `json:"completedCalls"`
	LatencyMilliseconds   []float64 `json:"latencyMilliseconds"`
	Samples               []Sample  `json:"samples"`
	Failures              int       `json:"failures"`
	SourceTimestampAbsent bool      `json:"sourceTimestampAbsent"`
	UnexpectedCoResidents []string  `json:"unexpectedCoResidents"`
	CoResidentWindowKnown bool      `json:"coResidentWindowKnown"`
	ThermalStateKnown     bool      `json:"thermalStateKnown"`
	ThermallyThrottled    bool      `json:"thermallyThrottled"`
	SteadyWindowVerified  bool      `json:"steadyWindowVerified"`
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
	RejectUnexpectedCoResidents    bool          `json:"rejectUnexpectedCoResidents"`
}
type AdmissionResult struct {
	Verdict         Verdict  `json:"verdict"`
	PolicyVersion   string   `json:"policyVersion"`
	StateIdentity   string   `json:"stateIdentity"`
	TrialID         string   `json:"trialID"`
	Reasons         []Reason `json:"reasons"`
	TotalWallJoules *float64 `json:"totalWallJoules,omitempty"`
	WindowSeconds   float64  `json:"windowSeconds"`
	CompletedItems  int      `json:"completedItems"`
}

type AdmittedTrial struct {
	StateIdentity                    string  `json:"stateIdentity"`
	TrialID                          string  `json:"trialID"`
	AdmissionPolicyVersion           string  `json:"admissionPolicyVersion"`
	EstimatorIdentity                string  `json:"estimatorIdentity"`
	BaselineUncertaintyJoulesPerItem float64 `json:"baselineUncertaintyJoulesPerItem"`
	Batch                            int     `json:"batch"`
	IncrementalJoulesPerItem         float64 `json:"incrementalJoulesPerItem"`
	LatencyP99Milliseconds           float64 `json:"latencyP99Milliseconds"`
}
type IdleTrial struct {
	StateIdentity          string  `json:"stateIdentity"`
	TrialID                string  `json:"trialID"`
	AdmissionPolicyVersion string  `json:"admissionPolicyVersion"`
	MeanWallWatts          float64 `json:"meanWallWatts"`
}
type IdleBaseline struct {
	StateIdentity          string      `json:"stateIdentity"`
	Trials                 []IdleTrial `json:"trials"`
	MeanWallWatts          float64     `json:"meanWallWatts"`
	ConfidenceHalfWidth    float64     `json:"confidenceHalfWidth"`
	PolicyVersion          string      `json:"policyVersion"`
	AdmissionPolicyVersion string      `json:"admissionPolicyVersion"`
}
type PointPolicy struct {
	Version          string  `json:"version"`
	MinimumTrials    int     `json:"minimumTrials"`
	ConfidenceMethod string  `json:"confidenceMethod"`
	ConfidenceLevel  float64 `json:"confidenceLevel"`
}
type Point struct {
	StateIdentity                string          `json:"stateIdentity"`
	Batch                        int             `json:"batch"`
	PointPolicyVersion           string          `json:"pointPolicyVersion"`
	EstimatorIdentity            string          `json:"estimatorIdentity"`
	ConfidenceMethod             string          `json:"confidenceMethod"`
	ConfidenceLevel              float64         `json:"confidenceLevel"`
	Trials                       []AdmittedTrial `json:"trials"`
	MeanIncrementalJoulesPerItem float64         `json:"meanIncrementalJoulesPerItem"`
	ConfidenceHalfWidth          float64         `json:"confidenceHalfWidth"`
	LatencyP99Milliseconds       float64         `json:"latencyP99Milliseconds"`
}

type BSatStatus string

const (
	BSatAccepted      BSatStatus = "Accepted"
	BSatCandidateOnly BSatStatus = "CandidateOnly"
	BSatCensored      BSatStatus = "Censored"
	BSatAmbiguous     BSatStatus = "Ambiguous"
	BSatUnavailable   BSatStatus = "Unavailable"
)

type BSatPolicy struct {
	Version                      string  `json:"version"`
	RelativeEquivalenceTolerance float64 `json:"relativeEquivalenceTolerance"`
}
type BSatResult struct {
	Status          BSatStatus `json:"status"`
	PolicyVersion   string     `json:"policyVersion"`
	StateIdentity   string     `json:"stateIdentity,omitempty"`
	Batch           *int       `json:"batch,omitempty"`
	Code            string     `json:"code"`
	MeasuredBatches []int      `json:"measuredBatches"`
}
