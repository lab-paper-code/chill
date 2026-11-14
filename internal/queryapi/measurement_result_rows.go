package queryapi

import (
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// MeasurementResultRow projects one independent measurement, never a mixed reused profile.
func MeasurementResultRow(facts profilederivation.MeasurementFacts, batch profilederivation.MeasurementBatchFacts, profileID string, reused, used bool) ResultRow {
	kind := "direct"
	if reused {
		kind = ResultRowConfirmation
	}
	row := ResultRow{
		ID: fmt.Sprintf("%s/%s/%d", profileID, kind, batch.Batch), RowKind: kind, ProfileID: profileID,
		Batch: batch.Batch, Condition: facts.Condition, P95Milliseconds: batch.P95Milliseconds,
		LatencySampleCount: &batch.LatencySampleCount, IdleWatts: batch.IdleWatts, ActiveWatts: batch.ActiveWatts,
		TotalJoulesPerItem: batch.TotalJoulesPerItem, IncrementalJoulesPerItem: batch.IncrementalJoulesPerItem,
		ActiveTrialCount: &batch.ActiveTrialCount, IdleTrialCount: &batch.IdleTrialCount,
		CompletedItems: &batch.CompletedItems, CompletedCalls: &batch.CompletedCalls,
		PowerSource: batch.PowerSource, Assessment: AssessMeasurementBatch(batch, facts.PointPolicy.MinimumTrials, reused, used),
		Sources: []string{}, MissingReasons: append([]string{}, batch.MissingReasons...),
	}
	for _, source := range batch.Sources {
		row.Sources = append(row.Sources, source.Store+"/"+source.Key)
	}
	if batch.Confidence != nil && batch.IncrementalJoulesPerItem != nil {
		ci, mean := batch.Confidence, *batch.IncrementalJoulesPerItem
		row.Confidence = &MeasurementConfidence{Level: ci.Level, Lower: mean - ci.HalfWidth, Upper: mean + ci.HalfWidth, HalfWidth: ci.HalfWidth}
	}
	return row
}
