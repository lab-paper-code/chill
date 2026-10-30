package kube

import (
	"maps"
	"slices"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

func cloneMeasurementFacts(facts serving.MeasurementFacts) serving.MeasurementFacts {
	facts.Condition = profilederivation.CloneCurvePlanSpec(facts.Condition)
	facts.Batches = slices.Clone(facts.Batches)
	for i := range facts.Batches {
		row := &facts.Batches[i]
		row.P95Milliseconds = cloneFactValue(row.P95Milliseconds)
		row.IdleWatts = cloneFactValue(row.IdleWatts)
		row.ActiveWatts = cloneFactValue(row.ActiveWatts)
		row.TotalJoulesPerItem = cloneFactValue(row.TotalJoulesPerItem)
		row.IncrementalJoulesPerItem = cloneFactValue(row.IncrementalJoulesPerItem)
		if row.Confidence != nil {
			confidence := *row.Confidence
			confidence.VarianceOfMean = cloneFactValue(confidence.VarianceOfMean)
			confidence.EffectiveDegreesOfFreedom = cloneFactValue(confidence.EffectiveDegreesOfFreedom)
			row.Confidence = &confidence
		}
		row.PowerSource.MetricLabels = maps.Clone(row.PowerSource.MetricLabels)
		row.Quality = slices.Clone(row.Quality)
		for j := range row.Quality {
			quality := &row.Quality[j]
			quality.PolicyPassed = cloneFactValue(quality.PolicyPassed)
			if quality.Protocol.Dataset != nil {
				dataset := *quality.Protocol.Dataset
				quality.Protocol.Dataset = &dataset
			}
		}
		row.Sources = slices.Clone(row.Sources)
		row.MissingReasons = slices.Clone(row.MissingReasons)
	}
	return facts
}

func cloneFactValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
