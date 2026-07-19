package operatorconsole

import (
	"errors"
	"math"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/powerobserver"
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
	Runtime EvidencePassportRuntime `json:"runtime"`
	Power   EvidencePassportPower   `json:"power"`
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
	watts := summarizeWatts(snapshot.Measurement.Power.Samples)
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
				ElapsedSeconds:  runtime.MeasurementElapsedSeconds,
				CompletedCalls:  runtime.InferenceCount,
				CompletedItems:  runtime.InferenceCount * runtime.BatchSize,
				ThroughputItems: runtime.ThroughputItemsPerSecond,
				Latency:         latency,
				OutputValidation: EvidencePassportOutputValidation{
					Method: runtime.OutputValidation.Method, Passed: runtime.OutputValidation.Passed,
				},
			},
			Power: EvidencePassportPower{
				Source: EvidencePassportPowerSource{
					Kind:     snapshot.Measurement.Power.Source.Kind,
					NodeName: snapshot.Measurement.Power.Source.NodeName,
					Metric:   snapshot.Measurement.Power.Source.Metric,
				},
				StartedAt:                 snapshot.Measurement.Power.StartedAt.UTC(),
				EndedAt:                   snapshot.Measurement.Power.EndedAt.UTC(),
				ObservationSeconds:        snapshot.Measurement.Power.Summary.ObservationDurationSeconds,
				Samples:                   snapshot.Measurement.Power.Summary.SuccessfulSamples,
				Failures:                  snapshot.Measurement.Power.Summary.Failures,
				Watts:                     watts,
				MeanRequestLatencySeconds: snapshot.Measurement.Power.Summary.MeanRequestLatencySeconds,
				P95RequestLatencySeconds:  snapshot.Measurement.Power.Summary.P95RequestLatencySeconds,
				MaximumSampleGapSeconds:   snapshot.Measurement.Power.Summary.MaximumSampleGapSeconds,
				SourceTimestampAbsent:     snapshot.Measurement.Power.Summary.SourceTimestampAbsent,
			},
		},
		Admission: run.Status.Admission,
	}, nil
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

func summarizeWatts(samples []powerobserver.Sample) *EvidencePassportWatts {
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
