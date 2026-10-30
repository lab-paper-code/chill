package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const measurementVerdictAccepted = "Accepted"

func (o *measurementService) assessList(
	ctx context.Context, list api.BatchEnergyOptimizationList,
) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := httpapi.ProfilesView{}
	if o.scheduler != nil {
		if snapshot := o.scheduler.profiles.Load(); snapshot != nil {
			view = *snapshot
		}
	}
	base := &measurementCoverageBase{requests: list}
	if len(list.Items) > 0 {
		if err := o.client.List(ctx, &base.runs, client.InNamespace(list.Items[0].Namespace)); err != nil {
			return nil, err
		}
	}
	owners := coverageOwnedRuns(base)
	assessments := make([]json.RawMessage, len(list.Items))
	for i, task := range list.Items {
		var err error
		assessment := assessMeasurementRequest(task, view)
		assessment.FailureSummary = operatorconsole.MeasurementRequestFailures(task, owners[string(task.UID)], assessment)
		assessments[i], err = json.Marshal(assessment)
		if err != nil {
			return nil, err
		}
	}
	return assessments, nil
}

// Each request retains its own execution state. A newer failed request never
// erases an older request's still-published catalog evidence.
func assessMeasurementRequest(
	task api.BatchEnergyOptimization, view httpapi.ProfilesView,
) operatorconsole.MeasurementAssessment {
	input := operatorconsole.MeasurementAssessmentInput{
		Execution: &operatorconsole.MeasurementExecution{
			Phase: task.Status.Phase, Generation: task.Generation,
			ObservedGeneration: task.Status.ObservedGeneration,
		},
		Attempted: true,
	}
	id := task.Namespace + "/" + task.Name + "/" + string(task.UID)
	measured := map[int]bool{}
	planned := make([]int, 0, len(task.Spec.BatchSizes))
	for _, batch := range task.Spec.BatchSizes {
		planned = append(planned, int(batch))
	}
	for _, profile := range view.Profiles {
		reused := profile.Reuse != nil && profile.Reuse.ConfirmationProfileID == id &&
			profile.Reuse.Outcome == measurementVerdictAccepted
		if profile.ID != id && !reused {
			continue
		}
		input.Accepted, input.Available, input.Reused = true, true, reused
		used := true
		input.SchedulerUsed = &used
		input.Facts = append(input.Facts,
			"Catalog profile: "+profile.ID,
			"Retained result: "+profile.Reference.Store+"/"+profile.Reference.Key,
		)
		for _, batch := range profile.Batches {
			measured[batch.Batch] = true
		}
		if facts, found := view.MeasurementFacts[profile.ID]; found {
			if len(planned) == 0 {
				planned = facts.Condition.PlannedBatchSizes
			}
			for _, batch := range facts.Batches {
				assessment := operatorconsole.AssessMeasurementBatch(batch, facts.PointPolicy.MinimumTrials, reused, true)
				input.Checks = append(input.Checks, assessment.Checklist.Checks...)
				input.Problems = append(input.Problems, assessment.Reasons...)
			}
		}
	}
	if !input.Accepted {
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
			if len(planned) == 0 {
				planned = facts.Condition.PlannedBatchSizes
			}
			for _, batch := range facts.Batches {
				measured[batch.Batch] = true
				assessment := operatorconsole.AssessMeasurementBatch(batch, facts.PointPolicy.MinimumTrials, input.Reused, false)
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
			if issue.Code == "ReplacementSelection" && strings.HasPrefix(issue.Reason, "superseded by ") {
				input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
					Code: issue.Code, Message: issue.Reason, SourceIDs: []string{id},
				})
				continue
			}
			input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
				Code: issue.Code, Message: issue.Reason, SourceIDs: []string{id},
			})
		}
	}
	if !input.Accepted {
		input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
			Code: "AcceptedEvidenceUnavailable", Message: "No verified catalog profile for this request identity",
			SourceIDs: []string{id},
		})
	}
	return operatorconsole.AssessMeasurement(input)
}

func assessOperationCost(
	cr api.OperationCostMeasurement, steps []servingkube.OperationStepProjection,
) operatorconsole.MeasurementAssessment {
	input := operatorconsole.MeasurementAssessmentInput{
		Execution:        &operatorconsole.MeasurementExecution{Phase: string(cr.Status.Phase), Generation: cr.Generation},
		ExecutionCurrent: true, Attempted: true,
	}
	if cr.Status.Reason != "" {
		input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
			Code: string(cr.Status.Phase), Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	input.Accepted = len(cr.Spec.Steps) > 0 && len(cr.Status.Steps) == len(cr.Spec.Steps) &&
		len(steps) == len(cr.Status.Steps)
	if cr.Status.Phase == api.MeasurementPhaseInsufficient {
		input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
			Code: "OperationEvidenceInsufficient", Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	for i, step := range steps {
		available := step.OperationID != ""
		input.Available = input.Available || available
		accepted := available && step.WallJoules != nil && step.DurationNanos != nil &&
			cr.Status.Steps[i].Verdict == measurementVerdictAccepted
		input.Accepted = input.Accepted && accepted
		if available {
			input.Facts = append(input.Facts, "Stored operation step: "+cr.Status.Steps[i].Key)
		}
		if step.Reason != "" {
			input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
				Code: cr.Status.Steps[i].Verdict, Message: step.Reason,
				SourceIDs: []string{cr.Status.Steps[i].Key},
			})
		}
	}
	input.Facts = append(input.Facts, "OperationCostMeasurement has immutable spec and no observedGeneration status field")
	return operatorconsole.AssessMeasurement(input)
}

func (o *measurementService) projectDispatchOccupancy(
	ctx context.Context, cr api.DispatchOccupancyMeasurement,
) (httpapi.MeasurementProjection, error) {
	projection := httpapi.ProjectDispatchOccupancyMeasurement(cr)
	input := operatorconsole.MeasurementAssessmentInput{
		Execution:        &operatorconsole.MeasurementExecution{Phase: string(cr.Status.Phase), Generation: cr.Generation},
		ExecutionCurrent: true, Attempted: true,
		Facts: []string{"DispatchOccupancyMeasurement has immutable spec and no observedGeneration status field"},
	}
	if cr.Status.Reason != "" {
		input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
			Code: string(cr.Status.Phase), Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	if cr.Status.Phase == api.MeasurementPhaseInsufficient {
		input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
			Code: "OccupancyEvidenceInsufficient", Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	if cr.Status.PerRequestNanos != nil && *cr.Status.PerRequestNanos < 0 {
		input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
			Code: "OccupancyNegativeD", Message: "Signed negative d requires review; planner consumes max(d,0)",
			SourceIDs: []string{string(cr.UID)},
		})
	}
	if ref := cr.Status.Result; ref != nil && ref.Key != "" {
		raw, err := o.readDeletionEvidence(ctx, evidencestore.ObjectLocation{Store: ref.Store, Key: ref.Key}, false)
		if err != nil {
			code, message := "OccupancyEvidenceUnavailable", "분배 점유 근거를 읽지 못했습니다. 저장소 연결과 읽기 권한을 확인하세요."
			if errors.Is(err, evidencestore.ErrNotFound) {
				code, message = "OccupancyEvidenceMissing", "분배 점유 근거 파일이 없습니다. 원래 결과 위치와 보존 상태를 확인하세요."
			}
			input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
				Code: code, Message: message, SourceIDs: []string{ref.Key},
			})
			projection.Assessment, err = json.Marshal(operatorconsole.AssessMeasurement(input))
			return projection, err
		}
		var record servingkube.DispatchOccupancyRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
				Code: "OccupancyEvidenceInvalid", Message: "분배 점유 근거 파일을 해석하지 못했습니다. 보존된 원본을 확인하세요.", SourceIDs: []string{ref.Key},
			})
			projection.Assessment, err = json.Marshal(operatorconsole.AssessMeasurement(input))
			return projection, err
		}
		input.Available = true
		input.Facts = append(input.Facts, "Retained occupancy: "+ref.Store+"/"+ref.Key)
		matches := record.SchemaVersion == servingkube.DispatchOccupancySchema &&
			record.Node.Name == cr.Spec.Node && record.Node.UID != "" &&
			ref.Key == servingkube.DispatchOccupancyPrefix+record.Node.UID+"/"+string(cr.UID)+".json" &&
			reflect.DeepEqual(record.Conditions, cr.Spec) && record.Batch == int(cr.Spec.Batch) &&
			cr.Status.PerRequestNanos != nil && record.PerRequestNanos == *cr.Status.PerRequestNanos
		input.Accepted = matches && record.Verdict == measurementVerdictAccepted
		if !matches {
			input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
				Code: "OccupancyEvidenceMismatch", Message: "Stored occupancy does not match this request and status",
				SourceIDs: []string{ref.Key},
			})
		}
		if record.PerRequestNanos < 0 || math.IsNaN(record.Throughput) || math.IsInf(record.Throughput, 0) {
			input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
				Code:      "OccupancyValueProblem",
				Message:   "Signed negative d or nonfinite throughput requires review; stored values and verdict are unchanged",
				SourceIDs: []string{ref.Key},
			})
		}
		for _, reason := range record.Reasons {
			input.Reasons = append(input.Reasons, operatorconsole.MeasurementReason{
				Code: reason, Message: reason, SourceIDs: []string{ref.Key},
			})
		}
		input.Facts = append(input.Facts,
			fmt.Sprintf("Stored signed d: %d ns/request; planner consumes max(d,0)", record.PerRequestNanos),
			fmt.Sprintf("Stored physical cycles: %d; idle identified: %t", record.Cycles, record.IdleIdentified),
		)
		if record.PrecisionTargetMet != nil {
			input.Facts = append(input.Facts,
				fmt.Sprintf("Stored reporting precision target met: %t", *record.PrecisionTargetMet),
			)
		}
	}
	value, parseErr := strconv.ParseFloat(cr.Status.Throughput, 64)
	if cr.Status.Throughput != "" && (parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0)) {
		input.Problems = append(input.Problems, operatorconsole.MeasurementReason{
			Code: "OccupancyThroughputInvalid", Message: "Recorded throughput is not finite",
			SourceIDs: []string{string(cr.UID)},
		})
	}
	var err error
	projection.Assessment, err = json.Marshal(operatorconsole.AssessMeasurement(input))
	return projection, err
}

func (o *measurementService) decorateProfiles(ctx context.Context, view httpapi.ProfilesView) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type batchSummary struct {
		Batch                 int     `json:"batch"`
		EnergyPerInputJoules  float64 `json:"energyPerInputJoules"`
		EnergyBelowResolution bool    `json:"energyBelowResolution,omitempty"`
		MemoryBytes           uint64  `json:"memoryBytes"`
		MeanWallWatts         float64 `json:"meanWallWatts,omitempty"`
	}
	type profileProjection struct {
		serving.Profile
		Batches    []batchSummary                        `json:"batches"`
		Assessment operatorconsole.MeasurementAssessment `json:"assessment"`
		ResultRows []operatorconsole.ResultRow           `json:"resultRows"`
	}
	profiles := make([]profileProjection, 0, len(view.Profiles))
	for _, profile := range view.Profiles {
		assessment, rows := assessCatalogProfile(view, profile)
		if profile.Reuse != nil {
			sourceID := profile.Reuse.SourceProfileID
			targetNamespace := strings.SplitN(profile.Reuse.ConfirmationProfileID, "/", 2)[0]
			if strings.HasPrefix(sourceID, targetNamespace+"/") {
				if sourceFacts, found := view.MeasurementFacts[sourceID]; found {
					source := serving.Profile{ID: sourceID}
					for _, batch := range sourceFacts.Batches {
						row := coverageResultRow(source, sourceFacts, batch, operatorconsole.ResultRowSourceReference)
						row.Reuse = profile.Reuse
						row.Assessment = operatorconsole.AssessMeasurementBatch(
							batch, sourceFacts.PointPolicy.MinimumTrials, false, false,
						)
						rows = append(rows, row)
					}
				}
			}
		}
		projected := profileProjection{
			Profile: profile, Assessment: assessment, ResultRows: rows,
			Batches: make([]batchSummary, 0, len(profile.Batches)),
		}
		for _, batch := range profile.Batches {
			projected.Batches = append(projected.Batches, batchSummary{
				batch.Batch, batch.EnergyPerInputJoules, batch.EnergyBelowResolution, batch.MemoryBytes, batch.MeanWallWatts,
			})
		}
		profiles = append(profiles, projected)
	}
	// The catalog contract (SCH-67: preparations, reserved, costs, baselines, …) is
	// preserved verbatim; decoration only replaces the profile rows.
	base, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	var projected map[string]json.RawMessage
	if err := json.Unmarshal(base, &projected); err != nil {
		return nil, err
	}
	rows, err := json.Marshal(profiles)
	if err != nil {
		return nil, err
	}
	projected["profiles"] = rows
	delete(projected, "measurementFacts") // raw sample facts stay internal; preparations remain (harness restore contract)
	return json.Marshal(projected)
}

func assessCatalogProfile(
	view httpapi.ProfilesView, profile serving.Profile,
) (operatorconsole.MeasurementAssessment, []operatorconsole.ResultRow) {
	used := true
	input := operatorconsole.MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: profile.Reuse != nil, SchedulerUsed: &used,
		Facts: []string{
			"Catalog profile: " + profile.ID,
			"Retained result: " + profile.Reference.Store + "/" + profile.Reference.Key,
		},
	}
	rows := []operatorconsole.ResultRow{}
	if facts, found := view.MeasurementFacts[profile.ID]; found {
		kind := "direct"
		if profile.Reuse != nil {
			kind = operatorconsole.ResultRowConfirmation
		}
		for _, batch := range facts.Batches {
			row := coverageResultRow(profile, facts, batch, kind)
			if profile.Reuse != nil {
				sourceID := profile.Reuse.SourceProfileID
				row.SourceProfileID, row.Reuse = &sourceID, profile.Reuse
			}
			rows = append(rows, row)
			input.Checks = append(input.Checks, row.Assessment.Checklist.Checks...)
			input.Problems = append(input.Problems, row.Assessment.Reasons...)
		}
	}
	if len(rows) == 0 {
		input.Checks = []operatorconsole.MeasurementCheck{{
			Code: "MeasurementFactsUnavailable", Result: operatorconsole.MeasurementUnknown,
			Message: "Reporting facts are unavailable in this catalog snapshot", SourceIDs: []string{profile.ID},
		}}
	}
	return operatorconsole.AssessMeasurement(input), rows
}
