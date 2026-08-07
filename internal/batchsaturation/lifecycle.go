package batchsaturation

import (
	"errors"
	"fmt"
)

// Decide resolves the shared progress rules and delegates only the measurement
// question. A capability rejection is not a failure: it is reported to the
// method as an observed boundary and never retried.
func Decide(snapshot Snapshot, method Method) (Decision, error) {
	if method == nil {
		return Decision{}, errors.New("a measurement method is required")
	}
	if err := snapshot.Validate(); err != nil {
		return Decision{}, fmt.Errorf("validate snapshot: %w", err)
	}
	observed := Observed{}
	attempts := make(map[Measurement]int)
	accepted := make(map[Measurement]struct{})
	for _, trial := range snapshot.Trials {
		if trial.Verdict == VerdictPending {
			return Decision{Action: ActionWait, Reason: "TrialInFlight"}, nil
		}
		if trial.CapabilityRejected {
			batch := trial.Measurement.Batch
			if observed.CapabilityBoundary == nil || batch < *observed.CapabilityBoundary {
				observed.CapabilityBoundary = &batch
			}
			continue
		}
		if trial.Attempt > attempts[trial.Measurement] {
			attempts[trial.Measurement] = trial.Attempt
		}
		if trial.Verdict == VerdictAccepted {
			if _, exists := accepted[trial.Measurement]; !exists {
				accepted[trial.Measurement] = struct{}{}
			}
			observed.Accepted = append(observed.Accepted, trial.Measurement)
		}
	}

	for _, trial := range snapshot.Trials {
		if trial.Verdict != VerdictFailed || trial.CapabilityRejected {
			continue
		}
		if _, admitted := accepted[trial.Measurement]; admitted {
			continue
		}
		next := attempts[trial.Measurement] + 1
		if next > snapshot.RetryLimit+1 {
			return Decision{Action: ActionFail, Reason: "RetryLimitExhausted"}, nil
		}
		measurement := trial.Measurement
		return Decision{Action: ActionCreateRun, Run: &measurement, Reason: "RetryAfterFailure"}, nil
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
