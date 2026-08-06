package admission

import (
	"time"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
)

func projectIdleObservation(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.IdleRawMeasurement,
) (profilederivation.IdleObservation, error) {
	windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	modelLoaded, err := time.Parse(time.RFC3339Nano, raw.Runtime.ModelLoadCompletedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	settlingStarted, err := time.Parse(time.RFC3339Nano, raw.Runtime.SettlingStartedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	settlingCompleted, err := time.Parse(time.RFC3339Nano, raw.Runtime.SettlingCompletedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	thermalKnown, thermallyThrottled := thermalState(raw.Metrics.Samples, windowStart, windowEnd)
	workload, err := projectWorkloadWindow(raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	identities, err := ProjectRunIdentities(execution.Intent, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	return profilederivation.IdleObservation{
		StateIdentity: identities.StateIdentity, EnvironmentIdentity: workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		RawEvidence: profilederivation.RawEvidenceIdentity{
			Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key,
			Digest: execution.RawMeasurement.Digest, Size: execution.RawMeasurement.Size,
		},
		WindowStart: windowStart, WindowEnd: windowEnd,
		ModelLoadedBeforeSettling: !modelLoaded.After(settlingStarted),
		SettlingWindowVerified: !settlingCompleted.After(windowStart) &&
			settlingCompleted.Sub(settlingStarted) >= time.Duration(execution.Intent.Measurement.SettlingDurationSeconds)*time.Second,
		InferenceCountAfterModelLoad: raw.Runtime.InferenceCountAfterModelLoad,
		ModelResidentAtEnd:           raw.Runtime.ModelResidentAtEnd,
		Samples:                      samples, Failures: len(raw.Metrics.Failures),
		SourceTimestampAbsent:    raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:      workload.Known, ThermalStateKnown: thermalKnown,
		ThermallyThrottled: thermallyThrottled,
	}, nil
}

func projectTrial(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.RawMeasurement,
) (profilederivation.Trial, error) {
	windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.Trial{}, err
	}
	latencies := make([]float64, 0, raw.Runtime.InferenceCount)
	for _, repetition := range raw.Runtime.RepetitionResults {
		latencies = append(latencies, repetition.LatencyMilliseconds...)
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	thermalKnown, thermallyThrottled := thermalState(raw.Metrics.Samples, windowStart, windowEnd)
	workload, err := projectWorkloadWindow(raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	identities, err := ProjectRunIdentities(execution.Intent, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return profilederivation.Trial{
		StateIdentity:               identities.StateIdentity,
		EnvironmentIdentity:         workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		Batch:                       execution.Intent.Measurement.Batch,
		WindowStart:                 windowStart,
		WindowEnd:                   windowEnd,
		CompletedCalls:              raw.Runtime.InferenceCount,
		CompletedItems:              raw.Runtime.InferenceCount * execution.Intent.Measurement.Batch,
		LatencyMilliseconds:         latencies,
		Samples:                     samples,
		Failures:                    len(raw.Metrics.Failures),
		SourceTimestampAbsent:       raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads:    workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:     workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:         workload.Known,
		ThermalStateKnown:           thermalKnown,
		ThermallyThrottled:          thermallyThrottled,
		// RawMeasurement.Validate has already required ValidateRuntime, which
		// proves warm-up completion precedes this runtime-owned measurement window.
		SteadyWindowVerified: true,
	}, nil
}
