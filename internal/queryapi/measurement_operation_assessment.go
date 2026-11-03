package queryapi

import api "github.com/lab-paper-code/chill/api/v1alpha1"

// OperationStepEvidence contains only facts consulted by the measurement assessment.
type OperationStepEvidence struct {
	OperationID   string
	DurationNanos *int64
	WallJoules    *float64
	Reason        string
}

func AssessOperationCost(cr api.OperationCostMeasurement, steps []OperationStepEvidence) MeasurementAssessment {
	input := MeasurementAssessmentInput{
		Execution:        &MeasurementExecution{Phase: string(cr.Status.Phase), Generation: cr.Generation},
		ExecutionCurrent: true, Attempted: true,
	}
	if cr.Status.Reason != "" {
		input.Reasons = append(input.Reasons, MeasurementReason{
			Code: string(cr.Status.Phase), Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	input.Accepted = len(cr.Spec.Steps) > 0 && len(cr.Status.Steps) == len(cr.Spec.Steps) &&
		len(steps) == len(cr.Status.Steps)
	if cr.Status.Phase == api.MeasurementPhaseInsufficient {
		input.Problems = append(input.Problems, MeasurementReason{
			Code: "OperationEvidenceInsufficient", Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	for i, step := range steps {
		available := step.OperationID != ""
		input.Available = input.Available || available
		accepted := available && step.WallJoules != nil && step.DurationNanos != nil &&
			cr.Status.Steps[i].Verdict == MeasurementVerdictAccepted
		input.Accepted = input.Accepted && accepted
		if available {
			input.Facts = append(input.Facts, "Stored operation step: "+cr.Status.Steps[i].Key)
		}
		if step.Reason != "" {
			input.Reasons = append(input.Reasons, MeasurementReason{
				Code: cr.Status.Steps[i].Verdict, Message: step.Reason, SourceIDs: []string{cr.Status.Steps[i].Key},
			})
		}
	}
	input.Facts = append(
		input.Facts,
		"OperationCostMeasurement has immutable spec and no observedGeneration status field",
	)
	return AssessMeasurement(input)
}
