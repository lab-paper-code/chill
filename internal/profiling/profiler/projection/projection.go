package projection

import (
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

func projectIdleObservation(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.IdleRawMeasurement,
) (profilederivation.IdleObservation, error) {
	windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	workload, err := projectWorkloadWindow(raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	identities, err := ProjectRunIdentities(execution.RunConfig, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	return profilederivation.IdleObservation{
		StateIdentity: identities.StateIdentity, EnvironmentIdentity: workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		RawEvidence: profilederivation.RawEvidenceIdentity{
			Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key,
			Size: execution.RawMeasurement.Size,
		},
		WindowStart: windowStart, WindowEnd: windowEnd,
		InferenceCountAfterModelLoad: raw.Runtime.InferenceCountAfterModelLoad,
		ModelResidentAtEnd:           raw.Runtime.ModelResidentAtEnd,
		Samples:                      samples, Failures: len(raw.Metrics.Failures),
		SourceTimestampAbsent:    raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  workload.ExternalWorkloadChanges,
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
	nativeSamples := make([]profilederivation.NativeServiceSample, 0, raw.Runtime.InferenceCount)
	for _, repetition := range raw.Runtime.RepetitionResults {
		latencies = append(latencies, repetition.LatencyMilliseconds...)
		for _, sample := range repetition.NativeSamples {
			nativeSamples = append(nativeSamples, profilederivation.NativeServiceSample{
				BatchID: sample.BatchID, BatchSize: sample.BatchSize,
				RunnerServiceMilliseconds: sample.RunnerServiceMilliseconds,
				BackendCallMilliseconds:   sample.BackendCallMilliseconds,
				NativeRequestMilliseconds: sample.NativeRequestMilliseconds,
			})
		}
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	workload, err := projectWorkloadWindow(raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	identities, err := ProjectRunIdentities(execution.RunConfig, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return profilederivation.Trial{
		StateIdentity:               identities.StateIdentity,
		EnvironmentIdentity:         workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		Batch:                       execution.RunConfig.Measurement.Batch,
		WindowStart:                 windowStart,
		WindowEnd:                   windowEnd,
		CompletedCalls:              raw.Runtime.InferenceCount,
		CompletedItems:              raw.Runtime.InferenceCount * execution.RunConfig.Measurement.Batch,
		LatencyMilliseconds:         latencies,
		NativeSamples:               nativeSamples,
		Samples:                     samples,
		Failures:                    len(raw.Metrics.Failures),
		SourceTimestampAbsent:       raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads:    workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:     workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:         workload.Known,
	}, nil
}
