package transitioncost_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

const mode15W = "MODE_15W"

func TestSummarizeBuildsCompleteDirectedTableInFrozenOrder(t *testing.T) {
	directions := testDirections()
	results := completeResults(t, directions)

	table, err := transitioncost.Summarize(directions, 5, results)
	if err != nil {
		t.Fatal(err)
	}
	record := table.Record()
	if record.SummaryMethod != transitioncost.DirectedSummaryMethod {
		t.Fatalf("summary method = %q, want %q", record.SummaryMethod, transitioncost.DirectedSummaryMethod)
	}
	if len(record.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(record.Entries))
	}
	for index, direction := range directions {
		if record.Entries[index].SourceState != direction.Source ||
			record.Entries[index].TargetState != direction.Target {
			t.Fatalf("entry %d direction = %#v -> %#v, want %#v", index,
				record.Entries[index].SourceState, record.Entries[index].TargetState, direction)
		}
	}

	forward := record.Entries[0]
	assertClose(t, "tau mean", forward.TauMilliseconds.Mean, 4000)
	assertClose(t, "tau sample standard deviation",
		forward.TauMilliseconds.SampleStandardDeviation, math.Sqrt(2.5)*1000)
	assertClose(t, "tau minimum", forward.TauMilliseconds.Minimum, 2000)
	assertClose(t, "tau maximum", forward.TauMilliseconds.Maximum, 6000)
	assertClose(t, "eta mean", forward.EtaJoules.Mean, 0)
	assertClose(t, "eta sample standard deviation",
		forward.EtaJoules.SampleStandardDeviation, math.Sqrt(10))
	assertClose(t, "eta minimum preserves sign", forward.EtaJoules.Minimum, -4)
	assertClose(t, "eta maximum", forward.EtaJoules.Maximum, 4)
	if forward.MaximumBoundaryMappingUncertainty != 5*time.Millisecond {
		t.Fatalf("maximum boundary uncertainty = %v, want 5ms",
			forward.MaximumBoundaryMappingUncertainty)
	}
	if forward.ServingSurvivedTrueCount != 3 || forward.ServingSurvivedFalseCount != 2 {
		t.Fatalf("survival counts = true:%d false:%d, want true:3 false:2",
			forward.ServingSurvivedTrueCount, forward.ServingSurvivedFalseCount)
	}
	if forward.AttemptCount != 8 || forward.AcceptedCount != 5 ||
		forward.VerdictCounts != (transitioncost.VerdictCounts{
			Accepted: 5, Invalid: 1, Failed: 1, Insufficient: 1,
		}) {
		t.Fatalf("forward counts = attempts:%d accepted:%d verdicts:%#v",
			forward.AttemptCount, forward.AcceptedCount, forward.VerdictCounts)
	}
	if record.Entries[1].AttemptCount != 8 ||
		record.Entries[1].VerdictCounts != (transitioncost.VerdictCounts{
			Accepted: 5, Invalid: 1, Failed: 1, Insufficient: 1,
		}) {
		t.Fatalf("reverse counts = attempts:%d verdicts:%#v",
			record.Entries[1].AttemptCount, record.Entries[1].VerdictCounts)
	}
	if forward.TargetIdleBaseline.State != directions[0].Target ||
		record.Entries[1].TargetIdleBaseline.State != directions[1].Target {
		t.Fatalf("target baselines do not follow direction targets: %#v", record.Entries)
	}
}

func TestSummarizeReturnsTypedInsufficientWithoutPartialTable(t *testing.T) {
	directions := testDirections()
	results := make([]transitioncost.Result, 0, 9)
	for index := 0; index < 4; index++ {
		results = append(results, deriveAttempt(t, directions[0], fmt.Sprintf("forward-%d", index),
			2+index, float64(index), true, acceptedAttempt, nil))
	}
	for index := 0; index < 5; index++ {
		results = append(results, deriveAttempt(t, directions[1], fmt.Sprintf("reverse-%d", index),
			2+index, float64(index), true, acceptedAttempt, nil))
	}

	table, err := transitioncost.Summarize(directions, 5, results)
	var insufficient *transitioncost.InsufficientAcceptedCountError
	if !errors.As(err, &insufficient) {
		t.Fatalf("error = %v, want InsufficientAcceptedCountError", err)
	}
	if insufficient.AcceptedCountTarget != 5 || len(insufficient.Directions) != 2 ||
		insufficient.Directions[0].VerdictCounts.Accepted != 4 ||
		insufficient.Directions[1].VerdictCounts.Accepted != 5 {
		t.Fatalf("insufficient progress = %#v", insufficient)
	}
	if record := table.Record(); record.SummaryMethod != "" || len(record.Entries) != 0 {
		t.Fatalf("insufficient result exposed a partial numeric table: %#v", record)
	}
}

func TestSummarizeRequiresAtLeastFiveAcceptedPerDirection(t *testing.T) {
	_, err := transitioncost.Summarize(testDirections(), 4, nil)
	assertErrorContains(t, err, "at least five")
}

func TestSummarizeRejectsEveryMixedScopeIdentity(t *testing.T) {
	directions := testDirections()
	tests := []struct {
		name   string
		mutate func(*transitioncost.FrozenContract, *transitioncost.RawObservation)
	}{
		{name: "Node name", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.Node.Name = "mixed-node"
			r.Node = f.Node
		}},
		{name: "Node UID", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.Node.UID = "mixed-node-uid"
			r.Node = f.Node
		}},
		{name: "DeviceClass", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.DeviceClass = "mixed-device-class"
			r.DeviceClass = f.DeviceClass
		}},
		{name: "actuator kind", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.Actuator.Kind = "mixed-actuator"
			r.Actuator = f.Actuator
		}},
		{name: "actuator target", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.Actuator.Target = "/mixed/target"
			r.Actuator = f.Actuator
		}},
		{name: "workload", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.Workload.UID = "mixed-workload-uid"
			r.Workload = f.Workload
			for index := range r.Readiness {
				r.Readiness[index].Workload = f.Workload
			}
			r.PreTransitionProcess.Workload = f.Workload
			r.PostTransitionProcess.Workload = f.Workload
		}},
		{name: "protocol", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.ProtocolIdentity = "mixed-protocol"
			r.ProtocolIdentity = f.ProtocolIdentity
		}},
		{name: "meter", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.MeterIdentity = "mixed-meter"
			r.MeterIdentity = f.MeterIdentity
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := deriveAttempt(t, directions[0], "first", 2, 0, true, acceptedAttempt, nil)
			mixed := deriveAttempt(t, directions[1], "mixed", 2, 0, true, acceptedAttempt,
				func(frozen *transitioncost.FrozenContract, raw *transitioncost.RawObservation) {
					test.mutate(frozen, raw)
				})
			_, err := transitioncost.Summarize(directions, 5, []transitioncost.Result{first, mixed})
			assertErrorContains(t, err, "frozen measurement scope")
		})
	}
}

func TestSummarizeRejectsThirdOrWrongDirection(t *testing.T) {
	directions := testDirections()
	thirdMode := directions[0]
	thirdMode.Target.PowerMode = mode30W
	result := deriveAttempt(t, thirdMode, "third-mode", 2, 0, true, acceptedAttempt, nil)
	_, err := transitioncost.Summarize(directions, 5, []transitioncost.Result{result})
	assertErrorContains(t, err, "undeclared direction")

	wrongOrder := []transitioncost.DirectedStatePair{directions[0], thirdMode}
	_, err = transitioncost.Summarize(wrongOrder, 5, nil)
	assertErrorContains(t, err, "exactly reverse")
}

func TestSummarizeRejectsDuplicateNodeActuationUID(t *testing.T) {
	direction := testDirections()[0]
	result := deriveAttempt(t, direction, "duplicate", 2, 0, true, acceptedAttempt, nil)
	_, err := transitioncost.Summarize(testDirections(), 5, []transitioncost.Result{result, result})
	assertErrorContains(t, err, "duplicates NodeActuation UID")
}

func TestSummarizeRejectsPerDirectionBaselineDrift(t *testing.T) {
	direction := testDirections()[0]
	tests := []struct {
		name   string
		mutate func(*transitioncost.FrozenContract, *transitioncost.RawObservation)
	}{
		{name: "identity", mutate: func(f *transitioncost.FrozenContract, r *transitioncost.RawObservation) {
			f.TargetIdleBaseline.EvidenceReference = "evidence://other-baseline"
			r.BaselineReference = f.TargetIdleBaseline.EvidenceReference
		}},
		{name: "value", mutate: func(f *transitioncost.FrozenContract, _ *transitioncost.RawObservation) {
			f.TargetIdleBaseline.MeanWallWatts++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := deriveAttempt(t, direction, "first", 2, 0, true, acceptedAttempt, nil)
			drifted := deriveAttempt(t, direction, "drifted", 2, 0, true, acceptedAttempt,
				func(frozen *transitioncost.FrozenContract, raw *transitioncost.RawObservation) {
					test.mutate(frozen, raw)
				})
			_, err := transitioncost.Summarize(testDirections(), 5,
				[]transitioncost.Result{first, drifted})
			assertErrorContains(t, err, "target idle baseline")
		})
	}
}

func TestDirectedTableOwnsCallerAndRecordSlices(t *testing.T) {
	directions := testDirections()
	results := completeResults(t, directions)
	table, err := transitioncost.Summarize(directions, 5, results)
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := table.Record().Entries[0]

	directions[0] = transitioncost.DirectedStatePair{}
	results[0] = transitioncost.Result{}
	firstRecord := table.Record()
	firstRecord.Entries[0] = transitioncost.DirectedEntry{}
	if got := table.Record().Entries[0]; got != wantFirst {
		t.Fatalf("directed table aliases a caller or returned slice: got %#v, want %#v", got, wantFirst)
	}
}

type attemptOutcome int

const (
	acceptedAttempt attemptOutcome = iota
	invalidAttempt
	failedAttempt
	insufficientAttempt
)

func completeResults(
	t *testing.T,
	directions []transitioncost.DirectedStatePair,
) []transitioncost.Result {
	t.Helper()
	results := make([]transitioncost.Result, 0, 16)
	forwardEta := []float64{-4, -2, 0, 2, 4}
	reverseEta := []float64{-10, -5, 0, 5, 10}
	for directionIndex, direction := range directions {
		etas := forwardEta
		if directionIndex == 1 {
			etas = reverseEta
		}
		for index, eta := range etas {
			results = append(results, deriveAttempt(
				t, direction, fmt.Sprintf("direction-%d-accepted-%d", directionIndex, index),
				2+index, eta, index < 3, acceptedAttempt,
				func(_ *transitioncost.FrozenContract, raw *transitioncost.RawObservation) {
					raw.BoundaryMappingUncertainty = time.Duration(index+1) * time.Millisecond
				},
			))
		}
		for outcome := invalidAttempt; outcome <= insufficientAttempt; outcome++ {
			results = append(results, deriveAttempt(
				t, direction, fmt.Sprintf("direction-%d-outcome-%d", directionIndex, outcome),
				2, 0, true, outcome, nil,
			))
		}
	}
	return results
}

func deriveAttempt(
	t *testing.T,
	direction transitioncost.DirectedStatePair,
	id string,
	tauSeconds int,
	etaJoules float64,
	servingSurvived bool,
	outcome attemptOutcome,
	mutate func(*transitioncost.FrozenContract, *transitioncost.RawObservation),
) transitioncost.Result {
	t.Helper()
	frozen, raw, policy := validInputs()
	frozen.SourceState = direction.Source
	frozen.TargetState = direction.Target
	frozen.NodeActuation.Name = id
	frozen.NodeActuation.UID = id + "-uid"
	frozen.TargetIdleBaseline = baselineFor(direction.Target)

	startedAt := testTime(10)
	completedAt := startedAt.Add(time.Duration(tauSeconds) * time.Second)
	raw.ObservationStartedAt = startedAt.Add(-2 * time.Second)
	raw.ObservationEndedAt = completedAt.Add(2 * time.Second)
	raw.NodeActuation = frozen.NodeActuation
	raw.SourceState = direction.Source
	raw.TargetState = direction.Target
	raw.BaselineReference = frozen.TargetIdleBaseline.EvidenceReference
	raw.BaselineState = direction.Target
	raw.SourceReadBack = transitioncost.StateReadBack{
		Fact: transitioncost.MappedFact{
			OccurredAt: startedAt.Add(-time.Second),
			MappedAt:   startedAt.Add(-time.Second),
		},
		State: direction.Source,
	}
	raw.TransitionStart = &transitioncost.MappedFact{OccurredAt: startedAt, MappedAt: startedAt}
	raw.TargetReadBack = &transitioncost.StateReadBack{
		Fact: transitioncost.MappedFact{
			OccurredAt: startedAt.Add(time.Second),
			MappedAt:   startedAt.Add(time.Second),
		},
		State: direction.Target,
	}
	raw.Readiness = []transitioncost.ReadyObservation{{
		ObservedAt: completedAt, Workload: frozen.Workload, Ready: true,
	}}
	raw.PreTransitionProcess.ObservedAt = startedAt.Add(-time.Second)
	raw.PostTransitionProcess.ObservedAt = completedAt.Add(time.Second)
	if !servingSurvived {
		raw.PostTransitionProcess.Process.ContainerID = "replacement-container"
	}
	watts := frozen.TargetIdleBaseline.MeanWallWatts + etaJoules/float64(tauSeconds)
	raw.PowerSamples = constantPowerSamples(startedAt, completedAt, watts)
	policy.MinimumCoverage = 0.85
	policy.MaximumReadyObservationDelay = time.Duration(tauSeconds) * time.Second

	switch outcome {
	case acceptedAttempt:
	case invalidAttempt:
		raw.SourceReadBack.State.PowerMode = "unexpected-mode"
	case failedAttempt:
		raw.Readiness = nil
	case insufficientAttempt:
		raw.BoundaryMappingUncertainty = policy.MaximumBoundaryUncertainty + time.Millisecond
	default:
		t.Fatalf("unknown test attempt outcome %d", outcome)
	}
	if mutate != nil {
		mutate(&frozen, &raw)
	}
	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatalf("derive attempt %q: %v", id, err)
	}
	wantVerdict := map[attemptOutcome]transitioncost.Verdict{
		acceptedAttempt:     transitioncost.VerdictAccepted,
		invalidAttempt:      transitioncost.VerdictInvalid,
		failedAttempt:       transitioncost.VerdictFailed,
		insufficientAttempt: transitioncost.VerdictInsufficient,
	}[outcome]
	if result.Verdict() != wantVerdict {
		t.Fatalf("attempt %q verdict = %s/%s (%s), want %s",
			id, result.Verdict(), result.Reason(), result.Detail(), wantVerdict)
	}
	return result
}

func baselineFor(target transitioncost.StateIdentity) transitioncost.IdleBaseline {
	meanWallWatts := 12.0
	if target.PowerMode == mode15W {
		meanWallWatts = 8
	}
	return transitioncost.IdleBaseline{
		EvidenceReference: "evidence://idle-" + strings.ToLower(target.PowerMode),
		State:             target,
		MeanWallWatts:     meanWallWatts,
	}
}

func constantPowerSamples(start, end time.Time, watts float64) []transitioncost.PowerSample {
	samples := []transitioncost.PowerSample{{ObservedAt: start.Add(100 * time.Millisecond), Watts: watts}}
	for observedAt := start.Add(time.Second); observedAt.Before(end.Add(-100 * time.Millisecond)); observedAt = observedAt.Add(time.Second) {
		samples = append(samples, transitioncost.PowerSample{ObservedAt: observedAt, Watts: watts})
	}
	samples = append(samples, transitioncost.PowerSample{ObservedAt: end.Add(-100 * time.Millisecond), Watts: watts})
	return samples
}

func testDirections() []transitioncost.DirectedStatePair {
	low := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: mode15W,
	}
	high := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MAXN",
	}
	return []transitioncost.DirectedStatePair{
		{Source: low, Target: high},
		{Source: high, Target: low},
	}
}

func assertClose(t *testing.T, field string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}
