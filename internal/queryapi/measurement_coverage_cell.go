package queryapi

import profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"

// AssessMeasurementCoverageCell assesses the selected current variant without
// falling back to older successes. selected is the shared selector's result,
// or nil when no candidate is current. Input slices and request history are retained.
func AssessMeasurementCoverageCell(
	cell MeasurementCoverageCell, selected *profilederivation.MeasurementSelection, sourcesComplete bool,
) MeasurementCoverageCell {
	current := []MeasurementCoverageVariant{}
	cell.Assessment = AssessMeasurement(MeasurementAssessmentInput{
		Support: cell.Support.Verdict, SourcesComplete: sourcesComplete,
	})
	if selected != nil {
		for _, variant := range cell.Variants {
			if variant.ID == selected.ID {
				current = []MeasurementCoverageVariant{variant}
				cell.Assessment = variant.Assessment
				break
			}
		}
	}
	cell.CurrentMeasurement = selected
	cell.Variants, cell.VariantCount = current, len(current)
	cell.BatchProgress, cell.Representative = nil, nil
	if len(current) == 1 {
		variant := &cell.Variants[0]
		cell.BatchProgress = &variant.BatchProgress
		for _, row := range variant.ResultRows {
			if row.RowKind == ResultRowSourceReference {
				continue
			}
			cell.Representative = &MeasurementRepresentative{
				VariantID: variant.ID, Batch: row.Batch,
				IncrementalJoulesPerItem: row.IncrementalJoulesPerItem, P95Milliseconds: row.P95Milliseconds,
			}
			break
		}
	}
	if cell.Support.Verdict == MeasurementSupportIncompatible && cell.Assessment.Evidence.Accepted {
		cell.Assessment = AssessMeasurement(MeasurementAssessmentInput{
			Accepted: true, Available: true, Problems: []MeasurementReason{{
				Code: "CurrentDeclarationConflict", Message: "보존 측정과 현재 지원 선언이 일치하지 않습니다", SourceIDs: []string{},
			}},
		})
	}
	return cell
}
