package batchenergyoptimization

import (
	"errors"
	"fmt"
)

// Decide resolves the shared progress rules and delegates only the measurement
// question. The planned batch sizes recorded for the measurement are passed
// through to the method.
func Decide(snapshot Snapshot, method Method) (Decision, error) {
	if method == nil {
		return Decision{}, errors.New("a measurement method is required")
	}
	if err := snapshot.Validate(); err != nil {
		return Decision{}, fmt.Errorf("validate snapshot: %w", err)
	}
	if snapshot.TransitionRequired {
		if snapshot.Transition == nil {
			return Decision{Action: ActionCreateTransition, Reason: "RequiredStateNotEstablished"}, nil
		}
		switch snapshot.Transition.Outcome {
		case TransitionInFlight:
			return Decision{Action: ActionWait, Reason: "TransitionInFlight"}, nil
		case TransitionFailed:
			return Decision{Action: ActionFail, Reason: "TransitionFailed"}, nil
		case TransitionEstablished:
		}
	}
	observed := Observed{PlannedBatchSizes: append([]int(nil), snapshot.PlannedBatchSizes...)}
	// A live trial must settle before a boundary can terminate the campaign.
	for _, trial := range snapshot.Trials {
		if trial.Verdict == VerdictPending {
			return Decision{Action: ActionWait, Reason: "TrialInFlight"}, nil
		}
		if trial.Verdict == VerdictCompleted {
			observed.Completed = append(observed.Completed, trial.Measurement)
		}
	}

	for _, trial := range snapshot.Trials {
		if trial.Verdict != VerdictFailed {
			continue
		}
		if trial.Boundary == nil || !trial.Boundary.isExecutionBoundary() {
			return Decision{Action: ActionFail, Reason: "TrialFailed"}, nil
		}
		if observed.Boundary == nil ||
			trial.Measurement.Batch < observed.Boundary.FailedMeasurement.Batch ||
			(trial.Measurement.Batch == observed.Boundary.FailedMeasurement.Batch &&
				trial.Attempt < observed.Boundary.Attempt) {
			boundary := *trial.Boundary
			measurement := trial.Measurement
			boundary.FailedMeasurement = &measurement
			boundary.Attempt = trial.Attempt
			observed.Boundary = &boundary
		}
	}

	plan, err := method.Plan(observed)
	if err != nil {
		return Decision{}, fmt.Errorf("plan next measurement: %w", err)
	}
	switch {
	case plan.Result != nil:
		return Decision{Action: ActionFinish, Result: plan.Result}, nil
	case plan.Next != nil:
		if err := plan.Next.Validate(); err != nil {
			return Decision{}, fmt.Errorf("validate planned measurement: %w", err)
		}
		next := *plan.Next
		return Decision{Action: ActionCreateRun, Run: &next, Reason: "MethodRequestedMeasurement"}, nil
	}
	return Decision{}, errors.New("method returned neither a next measurement nor a result")
}
