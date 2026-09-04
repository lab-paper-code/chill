package batchenergyoptimization

import (
	"reflect"
	"testing"
)

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

func directSnapshot() Snapshot {
	return Snapshot{
		RequiredState: State{TargetNode: "orangepi5", DeviceClass: "orangepi-5-plus-16g", PowerMode: "fixed"},
		RetryLimit:    2, PlannedBatchSizes: []int{1},
	}
}

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
			snapshot: directSnapshot(),
			plan:     Plan{Next: &first},
			want:     ActionCreateRun, wantRun: &first,
		},
		{
			name: "wait while a trial is live",
			snapshot: func() Snapshot {
				s := directSnapshot()
				s.Trials = []Trial{
					{Measurement: idle(1), Attempt: 1, Verdict: VerdictPending},
				}
				return s
			}(),
			want: ActionWait,
		},
		{
			name: "retry a failed attempt within the limit",
			snapshot: func() Snapshot {
				s := directSnapshot()
				s.Trials = []Trial{
					{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
				}
				return s
			}(),
			want: ActionCreateRun, wantRun: &first,
		},
		{
			name: "fail once the retry limit is exhausted",
			snapshot: func() Snapshot {
				s := directSnapshot()
				s.RetryLimit = 1
				s.Trials = []Trial{
					{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
					{Measurement: idle(1), Attempt: 2, Verdict: VerdictFailed},
				}
				return s
			}(),
			want: ActionFail,
		},
		{
			name: "finish when the method returns a result",
			snapshot: func() Snapshot {
				s := directSnapshot()
				s.Trials = []Trial{
					{Measurement: idle(1), Attempt: 1, Verdict: VerdictAccepted},
				}
				return s
			}(),
			plan: Plan{Result: &Result{Outcome: "MinimumFound/LocalMinimum"}},
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
	snapshot := directSnapshot()
	snapshot.Trials = []Trial{
		{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
		{Measurement: idle(1), Attempt: 2, Verdict: VerdictAccepted},
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
	}
	_, err := Decide(snapshot, method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(method.seen.Accepted) != 2 {
		t.Fatalf("method saw %d measurements, want only the admitted 2", len(method.seen.Accepted))
	}
}

func TestDecideGivesTheMethodThePlannedBatchSizes(t *testing.T) {
	method := &stubMethod{plan: Plan{Result: &Result{Outcome: "Stopped/CapabilityBoundary"}}}
	snapshot := directSnapshot()
	snapshot.RetryLimit = 1
	snapshot.PlannedBatchSizes = []int{1, 2, 4}
	snapshot.Trials = []Trial{
		{Measurement: idle(1), Attempt: 1, Verdict: VerdictAccepted},
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
	}

	if _, err := Decide(snapshot, method); err != nil {
		t.Fatalf("decide: %v", err)
	}

	if !reflect.DeepEqual(method.seen.PlannedBatchSizes, []int{1, 2, 4}) {
		t.Fatalf("planned batch sizes = %v, want [1 2 4]", method.seen.PlannedBatchSizes)
	}
}

func TestDecideEmitsOneTransitionForAWholeSweep(t *testing.T) {
	required := State{TargetNode: "jetsono", DeviceClass: "jetson-agx-orin-64g", PowerMode: "MODE_50W"}
	snapshot := Snapshot{
		RequiredState: required, TransitionRequired: true,
		RetryLimit: 1, PlannedBatchSizes: []int{1, 2},
	}
	measurements := []Measurement{idle(1), active(1), active(2)}
	method := &sweepMethod{measurements: measurements}
	transitionCount := 0
	runCount := 0

	decision, err := Decide(snapshot, method)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionCreateTransition {
		t.Fatalf("first action = %q, want %q", decision.Action, ActionCreateTransition)
	}
	transitionCount++

	snapshot.Transition = &Transition{Outcome: TransitionInFlight}
	decision, err = Decide(snapshot, method)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionWait {
		t.Fatalf("in-flight action = %q, want %q", decision.Action, ActionWait)
	}

	snapshot.Transition = &Transition{Outcome: TransitionEstablished, EstablishedState: &required}
	for {
		decision, err = Decide(snapshot, method)
		if err != nil {
			t.Fatal(err)
		}
		switch decision.Action {
		case ActionCreateTransition:
			transitionCount++
		case ActionCreateRun:
			runCount++
			snapshot.Trials = append(snapshot.Trials, Trial{
				Measurement: *decision.Run, Attempt: 1, Verdict: VerdictAccepted,
			})
		case ActionFinish:
			if transitionCount != 1 || runCount != len(measurements) {
				t.Fatalf("created %d transitions and %d Runs, want 1 and %d", transitionCount, runCount, len(measurements))
			}
			return
		default:
			t.Fatalf("unexpected sweep action %q", decision.Action)
		}
	}
}

func TestDecideMakesAFailedTransitionTerminalBeforeTrialRules(t *testing.T) {
	snapshot := directSnapshot()
	snapshot.TransitionRequired = true
	snapshot.Transition = &Transition{Outcome: TransitionFailed}
	snapshot.Trials = []Trial{{Measurement: idle(1), Attempt: 1, Verdict: VerdictPending}}
	method := &stubMethod{plan: Plan{Next: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}}}

	decision, err := Decide(snapshot, method)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionFail || decision.Reason != "TransitionFailed" {
		t.Fatalf("decision = %#v, want Fail/TransitionFailed", decision)
	}
	if len(method.seen.PlannedBatchSizes) != 0 {
		t.Fatal("failed transition reached the measurement method")
	}
}

type sweepMethod struct {
	measurements []Measurement
}

func (m *sweepMethod) Plan(observed Observed) (Plan, error) {
	if len(observed.Accepted) == len(m.measurements) {
		return Plan{Result: &Result{Outcome: "Stopped/CapabilityBoundary"}}, nil
	}
	next := m.measurements[len(observed.Accepted)]
	return Plan{Next: &next}, nil
}
