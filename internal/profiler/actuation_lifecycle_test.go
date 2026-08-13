package profiler

import "testing"

func TestDecideRunActuationGatesJobOnReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   RunActuationSnapshot
		want RunActuationDecision
	}{
		{name: "wait for target", in: RunActuationSnapshot{State: RunActuationPending}, want: RunActuationDecision{Action: RunActuationWaitForReady}},
		{name: "create Job only after ready", in: RunActuationSnapshot{State: RunActuationReady}, want: RunActuationDecision{Action: RunActuationCreateJob}},
		{name: "continue existing Job", in: RunActuationSnapshot{State: RunActuationReady, JobRecorded: true}, want: RunActuationDecision{Action: RunActuationContinue}},
		{name: "report handoff failure", in: RunActuationSnapshot{State: RunActuationFailed}, want: RunActuationDecision{Action: RunActuationReportFailure, Failure: RunActuationHandoffFailed}},
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

func TestRunActuationSnapshotRejectsJobBeforeReady(t *testing.T) {
	t.Parallel()
	if _, err := DecideRunActuation(RunActuationSnapshot{
		State: RunActuationPending, JobRecorded: true,
	}); err == nil {
		t.Fatal("expected recorded Job before target readiness to fail")
	}
}
