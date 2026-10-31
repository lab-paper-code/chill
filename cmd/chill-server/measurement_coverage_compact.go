package main

import "github.com/lab-paper-code/chill/internal/operatorconsole"

// Coverage carries each batch's numerical facts and raw source locations once.
// Checklist provenance references those result row IDs instead of copying every
// raw location into every check, parent cell and entity index.
func compactCoverageResponse(response *operatorconsole.MeasurementCoverageResponse) {
	for i := range response.Cells {
		cell := &response.Cells[i]
		compactCoverageAssessment(&cell.Assessment)
		for j := range cell.Variants {
			variant := &cell.Variants[j]
			checks := coverageVariantChecklist(variant)
			compactCoverageAssessment(&variant.Assessment)
			variant.Assessment.Checklist.Checks = checks
			for k := range variant.ResultRows {
				compactCoverageAssessment(&variant.ResultRows[k].Assessment)
			}
		}
	}
}

func compactCoverageAssessment(assessment *operatorconsole.MeasurementAssessment) {
	assessment.Checklist.Checks = []operatorconsole.MeasurementCheck{}
	if len(assessment.Scope.Facts) < 2 {
		return
	}
	seen := make(map[string]struct{}, len(assessment.Scope.Facts))
	facts := assessment.Scope.Facts[:0]
	for _, fact := range assessment.Scope.Facts {
		if _, exists := seen[fact]; exists {
			continue
		}
		seen[fact] = struct{}{}
		facts = append(facts, fact)
	}
	assessment.Scope.Facts = facts
}

func coverageVariantChecklist(variant *operatorconsole.MeasurementCoverageVariant) []operatorconsole.MeasurementCheck {
	type checkIdentity struct{ code, result, message string }
	checks := []operatorconsole.MeasurementCheck{}
	indices := map[checkIdentity]int{}
	for _, check := range variant.Assessment.Checklist.Checks {
		if check.Code == "DatasetInputContract" {
			check.SourceIDs = []string{}
			for _, row := range variant.ResultRows {
				if row.RowKind != operatorconsole.ResultRowSourceReference &&
					coverageContains(variant.BatchProgress.Target, row.Batch) {
					check.SourceIDs = append(check.SourceIDs, row.ID)
				}
			}
			indices[checkIdentity{check.Code, check.Result, check.Message}] = len(checks)
			checks = append(checks, check)
			break
		}
	}
	for _, row := range variant.ResultRows {
		if row.RowKind == operatorconsole.ResultRowSourceReference ||
			!coverageContains(variant.BatchProgress.Target, row.Batch) {
			continue
		}
		for _, check := range row.Assessment.Checklist.Checks {
			key := checkIdentity{check.Code, check.Result, check.Message}
			index, exists := indices[key]
			if !exists {
				index = len(checks)
				indices[key] = index
				check.SourceIDs = []string{}
				checks = append(checks, check)
			}
			checks[index].SourceIDs = append(checks[index].SourceIDs, row.ID)
		}
	}
	return checks
}
