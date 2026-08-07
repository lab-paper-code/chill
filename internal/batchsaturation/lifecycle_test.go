package batchsaturation

import "testing"

type stubMethod struct {
	plan Plan
	seen Observed
}

func (s *stubMethod) Plan(observed Observed) (Plan, error) {
	s.seen = observed
	return s.plan, nil
}

func active(batch int) Measurement {
	return Measurement{WorkloadMode: WorkloadModeActive, Batch: batch}
}

//nolint:unparam // Keep the active and idle test builders symmetric for later batch cases.
func idle(batch int) Measurement { return Measurement{WorkloadMode: WorkloadModeIdle, Batch: batch} }

func TestDecide(t *testing.T) {
	first := idle(1)
	for _, testCase := range []struct {
		name     string
		snapshot Snapshot
		plan     Plan
		want     Action
		wantRun  *Measurement
	}{
		{
			name:     "ask the method when nothing has run",
			snapshot: Snapshot{RetryLimit: 2},
			plan:     Plan{Next: &first},
			want:     ActionCreateRun, wantRun: &first,
		},
		{
			name: "wait while a trial is live",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictPending},
			}},
			want: ActionWait,
		},
		{
			name: "retry a failed attempt within the limit",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
			}},
			want: ActionCreateRun, wantRun: &first,
		},
		{
			name: "fail once the retry limit is exhausted",
			snapshot: Snapshot{RetryLimit: 1, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
				{Measurement: idle(1), Attempt: 2, Verdict: VerdictFailed},
			}},
			want: ActionFail,
		},
		{
			name: "finish when the method returns a result",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictAccepted},
			}},
			plan: Plan{Result: &Result{Outcome: "Stopped/NoFurtherImprovement"}},
			want: ActionFinish,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := Decide(testCase.snapshot, &stubMethod{plan: testCase.plan})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Action != testCase.want {
				t.Fatalf("action = %q, want %q", decision.Action, testCase.want)
			}
			if testCase.wantRun != nil && (decision.Run == nil || *decision.Run != *testCase.wantRun) {
				t.Fatalf("run = %+v, want %+v", decision.Run, testCase.wantRun)
			}
		})
	}
}

func TestDecideHidesFailedTrialsFromTheMethod(t *testing.T) {
	method := &stubMethod{plan: Plan{Next: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}}}
	_, err := Decide(Snapshot{RetryLimit: 2, Trials: []Trial{
		{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
		{Measurement: idle(1), Attempt: 2, Verdict: VerdictAccepted},
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
	}}, method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(method.seen.Accepted) != 2 {
		t.Fatalf("method saw %d measurements, want only the admitted 2", len(method.seen.Accepted))
	}
}

func TestDecideReportsCapabilityBoundaryToTheMethod(t *testing.T) {
	method := &stubMethod{plan: Plan{Result: &Result{Outcome: "Stopped/CapabilityBoundary"}}}
	_, err := Decide(Snapshot{RetryLimit: 2, Trials: []Trial{
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
		{Measurement: active(2), Attempt: 1, Verdict: VerdictFailed, CapabilityRejected: true},
	}}, method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method.seen.CapabilityBoundary == nil || *method.seen.CapabilityBoundary != 2 {
		t.Fatalf("capability boundary = %v, want 2", method.seen.CapabilityBoundary)
	}
}
