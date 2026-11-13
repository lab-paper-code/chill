package batchenergyoptimization

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConditionMeasurementComplete reports campaign progress on the native object.
	ConditionMeasurementComplete = "MeasurementComplete"

	ReasonTransitionFailed              = "TransitionFailed"
	ReasonRetryLimitExhausted           = "RetryLimitExhausted"
	ReasonEnergyObjectivePolicyMismatch = "EnergyObjectivePolicyMismatch"
	ReasonInvalidCampaignRun            = "InvalidCampaignRun"
)

// TerminalStatusCurrent reports whether the campaign recorded a final outcome
// for its current generation. Its reconciler stops there, so no curve plan,
// result or Run is written for the campaign afterwards.
func TerminalStatusCurrent(parent *edgev1alpha1.BatchEnergyOptimization) bool {
	if parent.Generation < 1 || parent.Status.ObservedGeneration != parent.Generation {
		return false
	}
	for _, condition := range parent.Status.Conditions {
		if condition.Type != ConditionMeasurementComplete || condition.ObservedGeneration != parent.Generation {
			continue
		}
		if parent.Status.Phase == string(edgev1alpha1.MeasurementPhaseComplete) {
			return parent.Status.Result != nil && condition.Status == metav1.ConditionTrue && condition.Reason == "ResultWritten"
		}
		if parent.Status.Phase == string(edgev1alpha1.MeasurementPhaseFailed) &&
			condition.Status == metav1.ConditionFalse && condition.Reason == parent.Status.Outcome {
			return condition.Reason == ReasonTransitionFailed ||
				condition.Reason == ReasonRetryLimitExhausted ||
				condition.Reason == string(FailureClassFatalFailure) ||
				condition.Reason == string(FailureClassUnknownFailure) ||
				condition.Reason == ReasonEnergyObjectivePolicyMismatch ||
				condition.Reason == ReasonInvalidCampaignRun ||
				condition.Reason == profilingrun.ResolutionReasonInvalidRunSpec ||
				condition.Reason == profilingrun.ResolutionReasonExecutionPolicyInvalid ||
				condition.Reason == profilingrun.ResolutionReasonActuationPolicyInvalid
		}
	}
	return false
}
