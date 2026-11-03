package queryapi

import "strings"

func ProjectMeasurementProfile(view CoverageCatalog, profile CoverageProfile) (MeasurementAssessment, []ResultRow) {
	assessment, rows := AssessCatalogProfile(view, profile)
	if profile.Reuse != nil {
		sourceID := profile.Reuse.SourceProfileID
		targetNamespace := strings.SplitN(profile.Reuse.ConfirmationProfileID, "/", 2)[0]
		if strings.HasPrefix(sourceID, targetNamespace+"/") {
			if sourceFacts, found := view.MeasurementFacts[sourceID]; found {
				for _, batch := range sourceFacts.Batches {
					row := CoverageResultRow(
						CoverageProfile{ID: sourceID},
						sourceFacts,
						batch,
						ResultRowSourceReference,
					)
					row.Reuse = profile.ReusePayload
					row.Assessment = AssessMeasurementBatch(batch, sourceFacts.PointPolicy.MinimumTrials, false, false)
					rows = append(rows, row)
				}
			}
		}
	}
	return assessment, rows
}
