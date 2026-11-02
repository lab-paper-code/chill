package metricobserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unsafe"
)

// Diagnostic slots come from the result and memory envelopes. A long transport
// or parser message is retained with explicit truncation metadata; its length
// never decides whether an otherwise valid observation window can finish.
const maximumThermalIssues = 6 // one per typed thermal metric

// Capacity is derived by the observer, never an experiment input.
type Capacity struct {
	Reads           int
	Events          int
	ResultBytes     int64
	MemoryBytes     int64
	DiagnosticBytes int
}

// RequiredCapacity admits the complete schedule against the existing worker
// result and memory budgets. Until adds an immediate producer-end read; retries
// replace periodic reads at the same Interval, not a second independent cadence.
func RequiredCapacity(r Request, source SourceIdentity, limits Limits, until bool) (Capacity, error) {
	if err := r.Validate(); err != nil {
		return Capacity{}, err
	}
	readCount := uint64(r.Duration/r.Interval) + 2 // immediate + ticker, including deadline race
	if until {
		readCount++
	}
	if readCount > uint64(math.MaxInt/2) {
		return Capacity{}, errors.New("metric observation schedule overflows retained capacity")
	}
	reads := int64(readCount)
	if limits.MaxResultBytes <= 0 || limits.MemoryBudgetBytes <= 0 {
		return Capacity{}, errors.New("metric observation requires result and memory byte budgets")
	}
	sample, failure := largestRecords(source)
	stamp := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	envelope := Result{Source: source, StartedAt: stamp, EndedAt: stamp, Samples: []Sample{}, Failures: []Failure{}, Summary: Summary{
		Attempts: math.MaxInt, SuccessfulSamples: math.MaxInt, Failures: math.MaxInt,
		ObservationDurationSeconds: -math.MaxFloat64, MeanRequestLatencySeconds: -math.MaxFloat64,
		P95RequestLatencySeconds: -math.MaxFloat64, MaximumSampleGapSeconds: -math.MaxFloat64,
	}}
	base, err := json.Marshal(envelope)
	if err != nil {
		return Capacity{}, fmt.Errorf("encode metric identity: %w", err)
	}
	sampleJSON, _ := json.Marshal(sample)
	failureJSON, _ := json.Marshal(failure)
	// UTC RFC3339Nano is at most 30 bytes. Strings use the actual source
	// identity and worst-case six-byte JSON escaping per diagnostic/metric byte.
	// Reserve 32 bytes per float in addition to the encoded specimen: binary64
	// needs at most 17 significant digits, a sign, decimal point and exponent.
	perReadJSON := int64(len(sampleJSON) + len(failureJSON) + 2 + 4*32) // four float fields, array commas
	baseBytes := int64(len(base) + 4*32)                                // four summary float fields
	if baseBytes > limits.MaxResultBytes || reads > (limits.MaxResultBytes-baseBytes)/perReadJSON {
		return Capacity{}, errors.New("metric observation schedule exceeds result byte budget")
	}
	resultBytes := baseBytes + reads*perReadJSON
	perReadMemory := int64(unsafe.Sizeof(Sample{})+unsafe.Sizeof(Failure{})+unsafe.Sizeof(ThermalReading{})) +
		6*int64(unsafe.Sizeof(uint64(0))) + // separately allocated scalar pointers, including bool allocator rounding
		maximumThermalIssues*int64(unsafe.Sizeof(MetricIssue{})) +
		int64(maximumThermalIssues*maximumMetricBytes(source)) +
		2*int64(unsafe.Sizeof(float64(0))) // Summarize sort scratch
	// Fixed-capacity slices avoid append growth. Include worst-case JSON string
	// storage, encoder growth (up to 2x) and returned Marshal copy; 4x also leaves
	// one encoded record alive. This is evidence-buffer accounting, not Go RSS.
	baseMemory := int64(unsafe.Sizeof(Result{})) + baseBytes
	if resultBytes > (limits.MemoryBudgetBytes-baseMemory)/4 ||
		reads > (limits.MemoryBudgetBytes-baseMemory-4*resultBytes)/perReadMemory {
		return Capacity{}, errors.New("metric observation schedule exceeds memory byte budget")
	}
	memoryBytes := baseMemory + 4*resultBytes + reads*perReadMemory
	diagnosticsPerRead := int64(1) // one source/thermal endpoint failure
	if source.ThermalMetrics != nil {
		diagnosticsPerRead += maximumThermalIssues
	}
	slots := reads * diagnosticsPerRead
	// Every diagnostic byte costs at most six JSON bytes, one retained byte,
	// and four encoder/copy bytes per encoded byte. Neither is bytes/event.
	diagnosticBytes := min((limits.MaxResultBytes-resultBytes)/(6*slots),
		(limits.MemoryBudgetBytes-memoryBytes)/(25*slots))
	resultBytes += 6 * slots * diagnosticBytes
	memoryBytes += 25 * slots * diagnosticBytes
	return Capacity{Reads: int(reads), Events: int(2 * reads), ResultBytes: resultBytes,
		MemoryBytes: memoryBytes, DiagnosticBytes: int(diagnosticBytes)}, nil
}

func maximumMetricBytes(source SourceIdentity) int {
	if source.ThermalMetrics == nil {
		return 0
	}
	m := source.ThermalMetrics
	maximum := 0
	for _, name := range []string{m.ObservationAvailable, m.Active, m.CPUCoreThrottleEvents, m.CPUPackageThrottleEvents, m.Latched, m.MaximumTemperatureCelsius} {
		maximum = max(maximum, len(name))
	}
	return maximum
}

func largestRecords(source SourceIdentity) (Sample, Failure) {
	stamp := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	no := false
	counter := uint64(math.MaxUint64)
	temperature := -math.MaxFloat64
	thermal := &ThermalReading{ObservationAvailable: &no, Active: &no, Latched: &no, CPUCoreThrottleEvents: &counter, CPUPackageThrottleEvents: &counter, MaximumTemperatureCelsius: &temperature}
	issues := maximumThermalIssues
	if source.ThermalMetrics == nil {
		issues = 0
	}
	for range issues {
		thermal.Issues = append(thermal.Issues, MetricIssue{Metric: strings.Repeat("\x00", maximumMetricBytes(source)),
			Reason: FailureReasonInvalidReading, MessageBytes: math.MaxInt, MessageTruncated: true})
	}
	return Sample{ObservedAt: stamp, Watts: -math.MaxFloat64, Thermal: thermal, RequestLatencySeconds: -math.MaxFloat64},
		Failure{ObservedAt: stamp, RequestLatencySeconds: -math.MaxFloat64, Reason: FailureReasonSourceRead,
			MessageBytes: math.MaxInt, MessageTruncated: true}
}

func boundedFailure(at time.Time, latency time.Duration, err error, maximum int) Failure {
	message, originalBytes, truncated := boundedMessage(err.Error(), maximum)
	return Failure{ObservedAt: at, RequestLatencySeconds: latency.Seconds(), Reason: classify(err),
		Message: message, MessageBytes: originalBytes, MessageTruncated: truncated}
}

func boundedMessage(message string, maximum int) (string, int, bool) {
	if len(message) > maximum {
		return strings.Clone(message[:maximum]), len(message), true
	}
	return strings.Clone(message), 0, false
}

func checkThermal(reading ThermalReading, source SourceIdentity) error {
	if len(reading.Issues) > maximumThermalIssues {
		return fmt.Errorf("thermal reading exceeds typed metric issue bound: %d issues", len(reading.Issues))
	}
	for _, issue := range reading.Issues {
		if len(issue.Metric) > maximumMetricBytes(source) {
			return errors.New(issue.Metric)
		}
	}
	return validateThermalReading(&reading)
}
