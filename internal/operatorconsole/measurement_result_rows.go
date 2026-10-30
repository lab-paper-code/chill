package operatorconsole

import (
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// MeasurementResultRows projects independent measurements, never a mixed reused profile.
func MeasurementResultRows(facts profilederivation.MeasurementFacts, profileID string, reused, used bool) []ResultRow {
	rows := make([]ResultRow, 0, len(facts.Batches))
	for _, batch := range facts.Batches {
		rows = append(rows, MeasurementResultRow(facts, batch, profileID, reused, used))
	}
	return rows
}

func MeasurementResultRow(facts profilederivation.MeasurementFacts, batch profilederivation.MeasurementBatchFacts, profileID string, reused, used bool) ResultRow {
	kind := "direct"
	if reused {
		kind = "confirmation"
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
		row.Confidence = &MeasurementConfidence{Level: ci.Level, Lower: mean-ci.HalfWidth, Upper: mean+ci.HalfWidth, HalfWidth: ci.HalfWidth}
	}
	return row
}
