package profiler

import (
	"testing"
	"time"
)

func TestDecideRunActuationOrdersReadyJobRestoreAndEvidence(t *testing.T) {
	t.Parallel()
	finishedAt := time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC)
	restoreAt := finishedAt.Add(time.Second)
	tests := []struct {
		name string
		in   RunActuationSnapshot
		want RunActuationDecision
	}{
		{name: "wait for target", in: RunActuationSnapshot{State: RunActuationPending}, want: RunActuationDecision{Action: RunActuationWaitForReady}},
		{name: "create Job only after ready", in: RunActuationSnapshot{State: RunActuationReady}, want: RunActuationDecision{Action: RunActuationCreateJob}},
		{name: "hold target while Job runs", in: RunActuationSnapshot{State: RunActuationReady, JobRecorded: true, Job: JobActive}, want: RunActuationDecision{Action: RunActuationHoldTarget}},
		{name: "request restore after success", in: RunActuationSnapshot{State: RunActuationReady, JobRecorded: true, Job: JobSucceeded, JobFinishedAt: finishedAt}, want: RunActuationDecision{Action: RunActuationRequestRestore}},
		{name: "wait for requested restoration", in: RunActuationSnapshot{State: RunActuationRestoring, RestoreRequested: true, RestoreRequestedAt: restoreAt, JobRecorded: true, Job: JobSucceeded, JobFinishedAt: finishedAt}, want: RunActuationDecision{Action: RunActuationWaitForRestore}},
		{name: "continue after ordered restoration", in: RunActuationSnapshot{State: RunActuationRestored, RestoreRequested: true, RestoreRequestedAt: restoreAt, JobRecorded: true, Job: JobSucceeded, JobFinishedAt: finishedAt}, want: RunActuationDecision{Action: RunActuationContinue}},
		{name: "restore before final failed outcome", in: RunActuationSnapshot{State: RunActuationReady, JobRecorded: true, Job: JobFailed}, want: RunActuationDecision{Action: RunActuationRequestRestore}},
		{name: "report failed Job after restoration", in: RunActuationSnapshot{State: RunActuationRestored, RestoreRequested: true, RestoreRequestedAt: restoreAt, JobRecorded: true, Job: JobFailed}, want: RunActuationDecision{Action: RunActuationContinue}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecideRunActuation(test.in)
			if err != nil || got != test.want {
				t.Fatalf("DecideRunActuation() = %#v, %v, want %#v", got, err, test.want)
			}
		})
	}
}

func TestDecideRunActuationFailsClosedOnInterruptedOrUnorderedExecution(t *testing.T) {
	t.Parallel()
	finishedAt := time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC)
	restoreAt := finishedAt.Add(-time.Second)
	tests := []struct {
		name string
		in   RunActuationSnapshot
		want RunActuationFailure
	}{
		{name: "handoff failure", in: RunActuationSnapshot{State: RunActuationFailed}, want: RunActuationHandoffFailed},
		{name: "restored before Job", in: RunActuationSnapshot{State: RunActuationRestored, RestoreRequested: true, RestoreRequestedAt: restoreAt}, want: RunActuationEndedBeforeExecution},
		{name: "restored while Job active", in: RunActuationSnapshot{State: RunActuationRestored, RestoreRequested: true, RestoreRequestedAt: restoreAt, JobRecorded: true, Job: JobActive}, want: RunActuationInterruptedExecution},
		{name: "deadline restoration was not requested", in: RunActuationSnapshot{State: RunActuationRestored, JobRecorded: true, Job: JobSucceeded, JobFinishedAt: finishedAt}, want: RunActuationRestoreNotRequested},
		{name: "restore preceded Job success", in: RunActuationSnapshot{State: RunActuationRestored, RestoreRequested: true, RestoreRequestedAt: restoreAt, JobRecorded: true, Job: JobSucceeded, JobFinishedAt: finishedAt}, want: RunActuationRestoreTooEarly},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecideRunActuation(test.in)
			if err != nil || got.Action != RunActuationReportFailure || got.Failure != test.want {
				t.Fatalf("DecideRunActuation() = %#v, %v, want failure %s", got, err, test.want)
			}
		})
	}
}

func TestRunActuationSnapshotRejectsIncompleteOrderingFacts(t *testing.T) {
	t.Parallel()
	if _, err := DecideRunActuation(RunActuationSnapshot{
		State: RunActuationReady, JobRecorded: true, Job: JobSucceeded,
	}); err == nil {
		t.Fatal("expected successful Job without terminal timestamp to fail")
	}
	if _, err := DecideRunActuation(RunActuationSnapshot{
		State: RunActuationReady, RestoreRequested: true,
	}); err == nil {
		t.Fatal("expected restore request without timestamp to fail")
	}
}
