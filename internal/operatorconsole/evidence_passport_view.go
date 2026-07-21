package operatorconsole

import (
	"errors"
	"math"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const EvidencePassportAPIVersion = "console.chill.dacs.io/v1alpha1"

// EvidencePassport is the bounded, evidence-backed read model shared by the
// CLI and Dashboard. Raw latency and power arrays remain in Evidence Store.
type EvidencePassport struct {
	APIVersion  string                                    `json:"apiVersion"`
	Kind        string                                    `json:"kind"`
	ReadAt      time.Time                                 `json:"readAt"`
	Run         EvidencePassportRun                       `json:"run"`
	Evidence    EvidencePassportLineage                   `json:"evidence"`
	Measurement EvidencePassportMeasurement               `json:"measurement"`
	Admission   *edgev1alpha1.ProfilingRunAdmissionStatus `json:"admission,omitempty"`
}

type EvidencePassportRun struct {
	Namespace       string                                          `json:"namespace"`
	Name            string                                          `json:"name"`
	UID             string                                          `json:"uid"`
	ResourceVersion string                                          `json:"resourceVersion"`
	Generation      int64                                           `json:"generation"`
	Spec            edgev1alpha1.ProfilingRunSpec                   `json:"spec"`
	Conditions      []metav1.Condition                              `json:"conditions,omitempty"`
	Job             *edgev1alpha1.ProfilingRunObjectReference       `json:"job,omitempty"`
	ActualExecution *edgev1alpha1.ProfilingRunActualExecutionStatus `json:"actualExecution,omitempty"`
}

type EvidencePassportLineage struct {
	FinalizedAt time.Time                    `json:"finalizedAt"`
	Final       evidencestore.ObjectIdentity `json:"final"`
	Raw         evidencestore.ObjectIdentity `json:"raw"`
}

type EvidencePassportMeasurement struct {
	Runtime  EvidencePassportRuntime   `json:"runtime"`
	Power    EvidencePassportPower     `json:"power"`
	Thermal  EvidencePassportThermal   `json:"thermal"`
	Workload *EvidencePassportWorkload `json:"workload,omitempty"`
}

type EvidencePassportRuntime struct {
	Family             string                           `json:"family"`
	Version            string                           `json:"version"`
	Provider           string                           `json:"provider"`
	Architecture       string                           `json:"architecture"`
	Batch              int                              `json:"batch"`
	WarmupIterations   int                              `json:"warmupIterations"`
	MeasurementStarted time.Time                        `json:"measurementStartedAt"`
	MeasurementEnded   time.Time                        `json:"measurementEndedAt"`
	ElapsedSeconds     float64                          `json:"elapsedSeconds"`
	CompletedCalls     int                              `json:"completedCalls"`
	CompletedItems     int                              `json:"completedItems"`
	ThroughputItems    float64                          `json:"throughputItemsPerSecond"`
	Latency            EvidencePassportLatency          `json:"latencyMilliseconds"`
	OutputValidation   EvidencePassportOutputValidation `json:"outputValidation"`
}

// EvidencePassportOutputValidation keeps the Console contract independent from
// the complete runtime evidence schema while preserving the verified result.
type EvidencePassportOutputValidation struct {
	Method string `json:"method"`
	Passed bool   `json:"passed"`
}

type EvidencePassportLatency struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
}

type EvidencePassportPower struct {
	Source                    EvidencePassportPowerSource `json:"source"`
	StartedAt                 time.Time                   `json:"startedAt"`
	EndedAt                   time.Time                   `json:"endedAt"`
	ObservationSeconds        float64                     `json:"observationSeconds"`
	Samples                   int                         `json:"samples"`
	Failures                  int                         `json:"failures"`
	Watts                     *EvidencePassportWatts      `json:"watts,omitempty"`
	MeanRequestLatencySeconds float64                     `json:"meanRequestLatencySeconds"`
	P95RequestLatencySeconds  float64                     `json:"p95RequestLatencySeconds"`
	MaximumSampleGapSeconds   float64                     `json:"maximumSampleGapSeconds"`
	SourceTimestampAbsent     bool                        `json:"sourceTimestampAbsent"`
}

type EvidencePassportPowerSource struct {
	Kind     string `json:"kind"`
	NodeName string `json:"nodeName,omitempty"`
	Metric   string `json:"metric"`
}

// EvidencePassportWatts is an arithmetic summary of retained samples. It is
// not an idle-adjusted energy estimate and must not be presented as J/request.
type EvidencePassportWatts struct {
	Minimum float64 `json:"minimum"`
	Mean    float64 `json:"mean"`
	Maximum float64 `json:"maximum"`
}

// EvidencePassportThermal is a factual summary of retained typed samples.
// Scientific interpretation remains in the admission result.
type EvidencePassportThermal struct {
	Metrics                     *EvidencePassportThermalMetrics `json:"metrics,omitempty"`
	Samples                     int                             `json:"samples"`
	ObservationAvailableSamples int                             `json:"observationAvailableSamples"`
	ActiveSamples               int                             `json:"activeSamples"`
	LatchedSamples              int                             `json:"latchedSamples"`
	CPUCoreThrottleEvents       *EvidencePassportCounter        `json:"cpuCoreThrottleEvents,omitempty"`
	CPUPackageThrottleEvents    *EvidencePassportCounter        `json:"cpuPackageThrottleEvents,omitempty"`
	MaximumTemperatureCelsius   *EvidencePassportRange          `json:"maximumTemperatureCelsius,omitempty"`
	Issues                      int                             `json:"issues"`
}

type EvidencePassportThermalMetrics struct {
	ObservationAvailable      string `json:"observationAvailable"`
	Active                    string `json:"active"`
	CPUCoreThrottleEvents     string `json:"cpuCoreThrottleEvents"`
	CPUPackageThrottleEvents  string `json:"cpuPackageThrottleEvents"`
	Latched                   string `json:"latched"`
	MaximumTemperatureCelsius string `json:"maximumTemperatureCelsius"`
}

type EvidencePassportCounter struct {
	Samples int    `json:"samples"`
	First   uint64 `json:"first"`
	Last    uint64 `json:"last"`
}

type EvidencePassportRange struct {
	Samples int     `json:"samples"`
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

func BuildEvidencePassport(snapshot EvidencePassportSnapshot) (EvidencePassport, error) {
	if snapshot.Run.Status.Evidence == nil {
		return EvidencePassport{}, errors.New("Evidence Passport snapshot has no finalized evidence reference")
	}
	runtime := snapshot.Measurement.Runtime
	started, ended, err := runtime.MeasurementWindow()
	if err != nil {
		return EvidencePassport{}, err
	}
	latencies := make([]float64, 0, runtime.InferenceCount)
	for _, repetition := range runtime.RepetitionResults {
		latencies = append(latencies, repetition.LatencyMilliseconds...)
	}
	latency, err := summarizeLatency(latencies)
	if err != nil {
		return EvidencePassport{}, err
	}
	if runtime.BatchSize < 1 {
		return EvidencePassport{}, errors.New("verified runtime evidence contains an invalid batch size")
	}
	throughputItems := float64(runtime.BatchSize) * 1000 / latency.Mean
	watts := summarizeWatts(snapshot.Measurement.Metrics.Samples)
	thermal := summarizeThermal(snapshot.Measurement.Metrics)
	workload, err := summarizeWorkload(snapshot.Execution.Intent, snapshot.Measurement.Workload)
	if err != nil {
		return EvidencePassport{}, err
	}
	final := evidencestore.ObjectIdentity{
		Store:  snapshot.Run.Status.Evidence.Store,
		Key:    snapshot.Run.Status.Evidence.Key,
		Digest: snapshot.Run.Status.Evidence.Digest,
		Size:   snapshot.Run.Status.Evidence.Size,
	}
	run := snapshot.Run.DeepCopy()
	return EvidencePassport{
		APIVersion: EvidencePassportAPIVersion,
		Kind:       "EvidencePassport",
		ReadAt:     snapshot.ReadAt.UTC(),
		Run: EvidencePassportRun{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			ResourceVersion: run.ResourceVersion, Generation: run.Generation, Spec: run.Spec,
			Conditions: append([]metav1.Condition(nil), run.Status.Conditions...),
			Job:        run.Status.Job, ActualExecution: run.Status.ActualExecution,
		},
		Evidence: EvidencePassportLineage{
			FinalizedAt: snapshot.Execution.FinalizedAt.UTC(), Final: final,
			Raw: snapshot.Execution.RawMeasurement,
		},
		Measurement: EvidencePassportMeasurement{
			Runtime: EvidencePassportRuntime{
				Family: runtime.Runtime, Version: runtime.RuntimeVersion, Provider: runtime.Provider,
				Architecture: runtime.Architecture, Batch: runtime.BatchSize,
				WarmupIterations:   runtime.WarmupIterations,
				MeasurementStarted: started.UTC(), MeasurementEnded: ended.UTC(),
				ElapsedSeconds: runtime.MeasurementElapsedSeconds,
				CompletedCalls: runtime.InferenceCount,
				CompletedItems: runtime.InferenceCount * runtime.BatchSize,
				// Keep the runtime contract's batch/mean-latency definition, but
				// derive it again from the retained raw samples instead of trusting
				// the producer's summary field.
				ThroughputItems: throughputItems,
				Latency:         latency,
				OutputValidation: EvidencePassportOutputValidation{
					Method: runtime.OutputValidation.Method, Passed: runtime.OutputValidation.Passed,
				},
			},
			Power: EvidencePassportPower{
				Source: EvidencePassportPowerSource{
					Kind:     snapshot.Measurement.Metrics.Source.Kind,
					NodeName: snapshot.Measurement.Metrics.Source.NodeName,
					Metric:   snapshot.Measurement.Metrics.Source.Metric,
				},
				StartedAt:                 snapshot.Measurement.Metrics.StartedAt.UTC(),
				EndedAt:                   snapshot.Measurement.Metrics.EndedAt.UTC(),
				ObservationSeconds:        snapshot.Measurement.Metrics.Summary.ObservationDurationSeconds,
				Samples:                   snapshot.Measurement.Metrics.Summary.SuccessfulSamples,
				Failures:                  snapshot.Measurement.Metrics.Summary.Failures,
				Watts:                     watts,
				MeanRequestLatencySeconds: snapshot.Measurement.Metrics.Summary.MeanRequestLatencySeconds,
				P95RequestLatencySeconds:  snapshot.Measurement.Metrics.Summary.P95RequestLatencySeconds,
				MaximumSampleGapSeconds:   snapshot.Measurement.Metrics.Summary.MaximumSampleGapSeconds,
				SourceTimestampAbsent:     snapshot.Measurement.Metrics.Summary.SourceTimestampAbsent,
			},
			Thermal:  thermal,
			Workload: workload,
		},
		Admission: run.Status.Admission,
	}, nil
}

func summarizeThermal(result metricobserver.Result) EvidencePassportThermal {
	summary := EvidencePassportThermal{Metrics: cloneThermalMetrics(result.Source.ThermalMetrics)}
	var core, pack []uint64
	var temperatures []float64
	for _, sample := range result.Samples {
		reading := sample.Thermal
		if reading == nil {
			continue
		}
		summary.Samples++
		if reading.ObservationAvailable != nil && *reading.ObservationAvailable {
			summary.ObservationAvailableSamples++
		}
		if reading.Active != nil && *reading.Active {
			summary.ActiveSamples++
		}
		if reading.Latched != nil && *reading.Latched {
			summary.LatchedSamples++
		}
		if reading.CPUCoreThrottleEvents != nil {
			core = append(core, *reading.CPUCoreThrottleEvents)
		}
		if reading.CPUPackageThrottleEvents != nil {
			pack = append(pack, *reading.CPUPackageThrottleEvents)
		}
		if reading.MaximumTemperatureCelsius != nil {
			temperatures = append(temperatures, *reading.MaximumTemperatureCelsius)
		}
		summary.Issues += len(reading.Issues)
	}
	summary.CPUCoreThrottleEvents = summarizeCounter(core)
	summary.CPUPackageThrottleEvents = summarizeCounter(pack)
	summary.MaximumTemperatureCelsius = summarizeRange(temperatures)
	return summary
}

func cloneThermalMetrics(metrics *metricobserver.ThermalMetricIdentity) *EvidencePassportThermalMetrics {
	if metrics == nil {
		return nil
	}
	return &EvidencePassportThermalMetrics{
		ObservationAvailable:      metrics.ObservationAvailable,
		Active:                    metrics.Active,
		CPUCoreThrottleEvents:     metrics.CPUCoreThrottleEvents,
		CPUPackageThrottleEvents:  metrics.CPUPackageThrottleEvents,
		Latched:                   metrics.Latched,
		MaximumTemperatureCelsius: metrics.MaximumTemperatureCelsius,
	}
}

func summarizeCounter(values []uint64) *EvidencePassportCounter {
	if len(values) == 0 {
		return nil
	}
	return &EvidencePassportCounter{Samples: len(values), First: values[0], Last: values[len(values)-1]}
}

func summarizeRange(values []float64) *EvidencePassportRange {
	if len(values) == 0 {
		return nil
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	return &EvidencePassportRange{Samples: len(values), Minimum: minimum, Maximum: maximum}
}

func summarizeLatency(values []float64) (EvidencePassportLatency, error) {
	if len(values) == 0 {
		return EvidencePassportLatency{}, errors.New("verified runtime evidence contains no latency samples")
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var total float64
	for _, value := range sorted {
		total += value
	}
	return EvidencePassportLatency{
		Count: len(sorted), Mean: total / float64(len(sorted)),
		P50: nearestRank(sorted, 0.50), P95: nearestRank(sorted, 0.95), P99: nearestRank(sorted, 0.99),
	}, nil
}

func summarizeWatts(samples []metricobserver.Sample) *EvidencePassportWatts {
	if len(samples) == 0 {
		return nil
	}
	minimum, maximum := samples[0].Watts, samples[0].Watts
	var total float64
	for _, sample := range samples {
		total += sample.Watts
		minimum = math.Min(minimum, sample.Watts)
		maximum = math.Max(maximum, sample.Watts)
	}
	return &EvidencePassportWatts{Minimum: minimum, Mean: total / float64(len(samples)), Maximum: maximum}
}

func nearestRank(sorted []float64, quantile float64) float64 {
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
