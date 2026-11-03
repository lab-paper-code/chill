package state

import (
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	serving "github.com/lab-paper-code/chill/internal/serving"
	maps "maps"
	slices "slices"
)

// Only mutable ownership is copied; source facts are not decoded or validated
// again. Callers cannot mutate the admitted entry through returned slices/maps.
func CloneCatalogProfile(profile serving.Profile) serving.Profile {
	profile.Evidence = slices.Clone(profile.Evidence)
	profile.Batches = slices.Clone(profile.Batches)
	for i := range profile.Batches {
		profile.Batches[i].DurationNanos = slices.Clone(profile.Batches[i].DurationNanos)
	}
	profile.Condition = profilederivation.CloneCurvePlanSpec(profile.Condition)
	if profile.RuntimeMetadata != nil {
		metadata := *profile.RuntimeMetadata
		metadata.ModelFiles = slices.Clone(metadata.ModelFiles)
		metadata.Inputs = slices.Clone(metadata.Inputs)
		metadata.Outputs = slices.Clone(metadata.Outputs)
		metadata.Providers = slices.Clone(metadata.Providers)
		metadata.AvailableProviders = slices.Clone(metadata.AvailableProviders)
		metadata.CPUAffinity = slices.Clone(metadata.CPUAffinity)
		metadata.InitializedBatches = slices.Clone(metadata.InitializedBatches)
		metadata.RKNN = slices.Clone(metadata.RKNN)
		profile.RuntimeMetadata = &metadata
	}
	if profile.Reuse != nil {
		check := *profile.Reuse
		check.Batches = slices.Clone(check.Batches)
		if check.IdlePowerRelativeDifference != nil {
			value := *check.IdlePowerRelativeDifference
			check.IdlePowerRelativeDifference = &value
		}
		if check.Record != nil {
			reference := *check.Record
			check.Record = &reference
		}
		profile.Reuse = &check
	}
	profile.RuntimeDelivery = slices.Clone(profile.RuntimeDelivery)
	profile.MetricSource.MetricLabels = maps.Clone(profile.MetricSource.MetricLabels)
	if profile.MetricSource.ThermalMetrics != nil {
		thermal := *profile.MetricSource.ThermalMetrics
		profile.MetricSource.ThermalMetrics = &thermal
	}
	return profile
}
