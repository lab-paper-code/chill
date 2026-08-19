package controller

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeriveStatusPreservesConditionTimeUntilStatusTransitions(t *testing.T) {
	transition := metav1.NewTime(time.Date(2026, time.August, 11, 1, 2, 3, 0, time.UTC))
	previous := []metav1.Condition{{
		Type: batchSaturationConditionDone, Status: metav1.ConditionUnknown,
		LastTransitionTime: transition,
	}}
	status := deriveStatus(
		7,
		batchsaturation.Snapshot{Trials: []batchsaturation.Trial{{Verdict: batchsaturation.VerdictAccepted}}},
		batchsaturation.Decision{Action: batchsaturation.ActionWait, Reason: "TrialInFlight"},
		nil,
		previous,
	)
	if len(status.Conditions) != 1 || !status.Conditions[0].LastTransitionTime.Equal(&transition) {
		t.Fatalf("condition transition time = %v, want %v", status.Conditions, transition)
	}
	if status.AcceptedTrials != 1 || status.ObservedGeneration != 7 {
		t.Fatalf("derived status = %#v", status)
	}
}

func TestAcceptedRunsRequiresOneRunPerProjectedTrial(t *testing.T) {
	_, _, err := acceptedRuns("parent", batchsaturation.Snapshot{
		Trials: []batchsaturation.Trial{{Verdict: batchsaturation.VerdictAccepted}},
	}, nil)
	if err == nil {
		t.Fatal("trial/Run positional invariant mismatch was accepted")
	}
}
