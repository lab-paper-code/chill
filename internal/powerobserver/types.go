package powerobserver

import "time"

// FailureReason identifies why one source-read attempt failed.
type FailureReason string

const (
	FailureReasonTimeout        FailureReason = "Timeout"
	FailureReasonMetricMissing  FailureReason = "MetricMissing"
	FailureReasonInvalidReading FailureReason = "InvalidReading"
	FailureReasonSourceRead     FailureReason = "SourceReadFailed"
)

// SourceIdentity identifies the concrete source and metric observed in a run.
type SourceIdentity struct {
	Kind      string `json:"kind"`
	NodeName  string `json:"nodeName,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	PodName   string `json:"podName,omitempty"`
	Endpoint  string `json:"endpoint"`
	Metric    string `json:"metric"`
}

// Request defines one bounded power-observation window.
type Request struct {
	Interval       time.Duration
	Duration       time.Duration
	RequestTimeout time.Duration
}

// Sample is one successfully received power reading.
type Sample struct {
	// ObservedAt is assigned after the source response has been received and
	// parsed. It is not a device-side measurement timestamp.
	ObservedAt            time.Time `json:"observedAt"`
	Watts                 float64   `json:"watts"`
	RequestLatencySeconds float64   `json:"requestLatencySeconds"`
}

// Failure is one unsuccessful source-read attempt.
type Failure struct {
	ObservedAt            time.Time     `json:"observedAt"`
	RequestLatencySeconds float64       `json:"requestLatencySeconds"`
	Reason                FailureReason `json:"reason"`
	Message               string        `json:"message"`
}

// Summary contains facts derived only from a Result's samples and failures.
type Summary struct {
	Attempts                   int     `json:"attempts"`
	SuccessfulSamples          int     `json:"successfulSamples"`
	Failures                   int     `json:"failures"`
	ObservationDurationSeconds float64 `json:"observationDurationSeconds"`
	MeanRequestLatencySeconds  float64 `json:"meanRequestLatencySeconds"`
	P95RequestLatencySeconds   float64 `json:"p95RequestLatencySeconds"`
	MaximumSampleGapSeconds    float64 `json:"maximumSampleGapSeconds"`
	SourceTimestampAbsent      bool    `json:"sourceTimestampAbsent"`
}

// Result preserves the raw evidence and factual summary of one observation.
type Result struct {
	Source    SourceIdentity `json:"source"`
	StartedAt time.Time      `json:"startedAt"`
	EndedAt   time.Time      `json:"endedAt"`
	Samples   []Sample       `json:"samples"`
	Failures  []Failure      `json:"failures"`
	Summary   Summary        `json:"summary"`
}
