package metricobserver

import "time"

// FailureReason identifies why one source-read attempt failed.
type FailureReason string

const (
	FailureReasonTimeout        FailureReason = "Timeout"
	FailureReasonMetricMissing  FailureReason = "MetricMissing"
	FailureReasonInvalidReading FailureReason = "InvalidReading"
	FailureReasonSourceRead     FailureReason = "SourceReadFailed"
)

// SourceIdentity identifies the concrete source and typed metrics observed in a run.
type SourceIdentity struct {
	Kind           string                 `json:"kind"`
	NodeName       string                 `json:"nodeName,omitempty"`
	Namespace      string                 `json:"namespace,omitempty"`
	PodName        string                 `json:"podName,omitempty"`
	Endpoint       string                 `json:"endpoint"`
	Metric         string                 `json:"metric"`
	MetricLabels   map[string]string      `json:"metricLabels,omitempty"`
	ThermalMetrics *ThermalMetricIdentity `json:"thermalMetrics,omitempty"`
}

// ThermalMetricIdentity freezes the edge-metrics names interpreted by the adapter.
type ThermalMetricIdentity struct {
	ObservationAvailable      string `json:"observationAvailable"`
	Active                    string `json:"active"`
	CPUCoreThrottleEvents     string `json:"cpuCoreThrottleEvents"`
	CPUPackageThrottleEvents  string `json:"cpuPackageThrottleEvents"`
	Latched                   string `json:"latched"`
	MaximumTemperatureCelsius string `json:"maximumTemperatureCelsius"`
}

// Request defines one bounded metric-observation window.
type Request struct {
	Interval       time.Duration
	Duration       time.Duration
	RequestTimeout time.Duration
	// MaxEvents bounds the combined number of retained samples and failures.
	// Job composition derives it from the frozen observation window.
	MaxEvents int
}

// Reading is one successfully parsed source response. Power is required by the
// first profiling path; thermal facts remain explicit and heterogeneous.
type Reading struct {
	Watts   float64
	Thermal ThermalReading
}

// ThermalReading contains only facts returned in one edge-metrics response.
type ThermalReading struct {
	ObservationAvailable      *bool         `json:"observationAvailable,omitempty"`
	Active                    *bool         `json:"active,omitempty"`
	CPUCoreThrottleEvents     *uint64       `json:"cpuCoreThrottleEvents,omitempty"`
	CPUPackageThrottleEvents  *uint64       `json:"cpuPackageThrottleEvents,omitempty"`
	Latched                   *bool         `json:"latched,omitempty"`
	MaximumTemperatureCelsius *float64      `json:"maximumTemperatureCelsius,omitempty"`
	Issues                    []MetricIssue `json:"issues,omitempty"`
}

// MetricIssue preserves a missing or invalid typed metric without converting
// a successful power scrape into a whole-request failure.
type MetricIssue struct {
	Metric  string        `json:"metric"`
	Reason  FailureReason `json:"reason"`
	Message string        `json:"message"`
}

// Sample is one successfully received typed metric reading.
type Sample struct {
	// ObservedAt is assigned after the source response has been received and
	// parsed. It is not a device-side measurement timestamp.
	ObservedAt            time.Time       `json:"observedAt"`
	Watts                 float64         `json:"watts"`
	Thermal               *ThermalReading `json:"thermal,omitempty"`
	RequestLatencySeconds float64         `json:"requestLatencySeconds"`
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
