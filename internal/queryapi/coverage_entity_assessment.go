package queryapi

import (
	"reflect"
	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// Each request retains its own execution state. A newer failed request never
// erases an older request's still-published catalog evidence.
func AssessMeasurementRequest(
	task api.BatchEnergyOptimization, view, reporting CoverageCatalog,
) MeasurementAssessment {
	input := MeasurementAssessmentInput{
		Execution: &MeasurementExecution{
			Phase: task.Status.Phase, Generation: task.Generation,
			ObservedGeneration: task.Status.ObservedGeneration,
		},
		Attempted: true,
	}
	id := task.Namespace + "/" + task.Name + "/" + string(task.UID)
	measured := map[int]bool{}
	var planned []int
	for _, profile := range view.Profiles {
		reused := profile.Reuse != nil && profile.Reuse.ConfirmationProfileID == id &&
			profile.Reuse.Outcome == MeasurementVerdictAccepted
		if profile.ID != id && !reused {
			continue
		}
		input.Accepted, input.Available, input.Reused = true, true, reused
		used := MeasurementProfileCurrent(view, profile)
		input.SchedulerUsed = &used
		input.Facts = append(input.Facts,
			"Catalog profile: "+profile.ID,
			"Retained result: "+profile.Reference.Store+"/"+profile.Reference.Key,
		)
		for _, batch := range profile.Batches {
			measured[batch] = true
		}
		if facts, found := view.MeasurementFacts[profile.ID]; found {
			planned = measurementProfileTargets(&task, &facts, &profile)
			for _, batch := range facts.Batches {
				assessment := AssessMeasurementBatch(batch, facts.PointPolicy.MinimumTrials, reused, used)
				input.Checks = append(input.Checks, assessment.Checklist.Checks...)
				input.Problems = append(input.Problems, assessment.Reasons...)
			}
		}
	}
	if !input.Accepted {
		// Retained-but-unadopted evidence comes only from current reporting.
		// Never promote an old accepted snapshot into the latest facts.
		view = reporting
		for _, facts := range view.MeasurementFacts {
			if facts.Request.Namespace != task.Namespace || facts.Request.Name != task.Name ||
				facts.Request.UID != string(task.UID) || facts.ResultReference.Key == "" || len(facts.Batches) == 0 {
				continue
			}
			used := false
			input.Accepted, input.Available, input.SchedulerUsed = true, true, &used
			input.Reused = task.Annotations["chill.dacs.io/profile-reuse"] != ""
			input.Facts = append(input.Facts,
				"Verified retained result: "+facts.ResultReference.Store+"/"+facts.ResultReference.Key,
				"Retained result is not in the current scheduler profile selection",
			)
			profile := CoverageProfile{Condition: facts.Condition}
			planned = measurementProfileTargets(&task, &facts, &profile)
			for _, batch := range facts.Batches {
				measured[batch.Batch] = true
				assessment := AssessMeasurementBatch(
					batch,
					facts.PointPolicy.MinimumTrials,
					input.Reused,
					false,
				)
				input.Checks = append(input.Checks, assessment.Checklist.Checks...)
				input.Problems = append(input.Problems, assessment.Reasons...)
			}
			break
		}
	}
	for _, batch := range planned {
		if !measured[batch] {
			input.MissingBatches = append(input.MissingBatches, batch)
		}
	}
	for _, issue := range view.Insufficiencies {
		if issue.Source.Namespace == task.Namespace && issue.Source.Name == task.Name {
			if issue.Code == "ReplacementSelection" {
				input.Reasons = append(input.Reasons, MeasurementReason{
					Code: issue.Code, Message: issue.Reason, SourceIDs: []string{id},
				})
				continue
			}
			input.Problems = append(input.Problems, MeasurementReason{
				Code: issue.Code, Message: issue.Reason, SourceIDs: []string{id},
			})
		}
	}
	if !input.Accepted {
		input.Reasons = append(input.Reasons, MeasurementReason{
			Code: "AcceptedEvidenceUnavailable", Message: "No verified catalog profile for this request identity",
			SourceIDs: []string{id},
		})
	}
	return AssessMeasurement(input)
}

// measurementProfileTargets narrows only a current completed discovery whose
// verified facts and profile share the same retained condition and batch prefix.
func measurementProfileTargets(task *api.BatchEnergyOptimization, facts *profilederivation.MeasurementFacts, profile *CoverageProfile) []int {
	planned := facts.Condition.PlannedBatchSizes
	if task.Status.Phase != "Complete" || task.Status.ObservedGeneration != task.Generation ||
		task.Status.Outcome != string(batchenergyoptimization.BatchDiscoveryComplete) || profile.Reuse != nil ||
		facts.Request.Namespace != task.Namespace || facts.Request.Name != task.Name || facts.Request.UID != string(task.UID) ||
		facts.ResultReference.Key == "" || len(facts.Batches) == 0 || len(facts.Batches) > len(planned) ||
		!reflect.DeepEqual(&profile.Condition, &facts.Condition) {
		return planned
	}
	for i := range facts.Batches {
		if facts.Batches[i].Batch != planned[i] {
			return planned
		}
	}
	prefix := planned[:len(facts.Batches)]
	if len(profile.Batches) > 0 && !slices.Equal(profile.Batches, prefix) {
		return planned
	}
	return prefix
}

func AssessCatalogProfile(
	view CoverageCatalog, profile CoverageProfile,
) (MeasurementAssessment, []ResultRow) {
	used := MeasurementProfileCurrent(view, profile)
	input := MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: profile.Reuse != nil, SchedulerUsed: &used,
		Facts: []string{
			"Catalog profile: " + profile.ID,
			"Retained result: " + profile.Reference.Store + "/" + profile.Reference.Key,
		},
	}
	rows := []ResultRow{}
	if facts, found := view.MeasurementFacts[profile.ID]; found {
		kind := "direct"
		if profile.Reuse != nil {
			kind = ResultRowConfirmation
		}
		for _, batch := range facts.Batches {
			row := CoverageResultRow(profile, facts, batch, kind)
			row.Assessment.Scope.SchedulerUsed = &used
			if profile.Reuse != nil {
				sourceID := profile.Reuse.SourceProfileID
				row.SourceProfileID, row.Reuse = &sourceID, profile.ReusePayload
			}
			rows = append(rows, row)
			input.Checks = append(input.Checks, row.Assessment.Checklist.Checks...)
			input.Problems = append(input.Problems, row.Assessment.Reasons...)
		}
	}
	if len(rows) == 0 {
		input.Checks = []MeasurementCheck{{
			Code: "MeasurementFactsUnavailable", Result: MeasurementUnknown,
			Message: "Reporting facts are unavailable in this catalog snapshot", SourceIDs: []string{profile.ID},
		}}
	}
	return AssessMeasurement(input), rows
}

func MeasurementProfileCurrent(view CoverageCatalog, profile CoverageProfile) bool {
	id := profile.ID
	if profile.Reuse != nil {
		id = profile.Reuse.ConfirmationProfileID
	}
	for _, current := range view.CurrentMeasurements {
		if current.ID == id {
			return true
		}
	}
	return false
}
