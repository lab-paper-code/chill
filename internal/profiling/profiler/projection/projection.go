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
