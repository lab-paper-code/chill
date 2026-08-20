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
	if forward.MaximumBoundaryMappingUncertaintyMilliseconds != 5 {
		t.Fatalf("maximum boundary uncertainty = %dms, want 5ms",
			forward.MaximumBoundaryMappingUncertaintyMilliseconds)
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
	results := make([]transitioncost.FinalizedObservation, 0, 9)
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

func TestSummarizeRejectsThirdOrWrongDirection(t *testing.T) {
	directions := testDirections()
	thirdMode := directions[0]
	thirdMode.Target.PowerMode = mode30W
	result := deriveAttempt(t, thirdMode, "third-mode", 2, 0, true, acceptedAttempt, nil)
	_, err := transitioncost.Summarize(directions, 5, []transitioncost.FinalizedObservation{result})
	assertErrorContains(t, err, "undeclared direction")

	wrongOrder := []transitioncost.DirectedStatePair{directions[0], thirdMode}
	_, err = transitioncost.Summarize(wrongOrder, 5, nil)
	assertErrorContains(t, err, "exactly reverse")
}

func TestSummarizeRejectsPerDirectionBaselineDrift(t *testing.T) {
	direction := testDirections()[0]
	tests := []struct {
		name   string
		mutate func(*transitioncost.FrozenContract, *transitioncost.RawObservation)
	}{
		{name: "evidence reference", mutate: func(f *transitioncost.FrozenContract, _ *transitioncost.RawObservation) {
			f.TargetIdleBaseline.EvidenceReference = "evidence://other-baseline"
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
				[]transitioncost.FinalizedObservation{first, drifted})
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
	results[0] = transitioncost.FinalizedObservation{}
	firstRecord := table.Record()
	firstRecord.Entries[0] = transitioncost.DirectedEntry{}
	if got := table.Record().Entries[0]; got != wantFirst {
		t.Fatalf("directed table aliases a caller or returned slice: got %#v, want %#v", got, wantFirst)
	}
}

func TestRestoreDirectedTableValidatesStoredRecordAndOwnsIt(t *testing.T) {
	record := completeDirectedTableRecord(t)
	wantFirst := record.Entries[0]

	table, err := transitioncost.RestoreDirectedTable(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Entries[0] = transitioncost.DirectedEntry{}
	if got := table.Record().Entries[0]; got != wantFirst {
		t.Fatalf("restored table aliases stored record: got %#v, want %#v", got, wantFirst)
	}
}

func TestRestoreDirectedTableRejectsMalformedStoredRecord(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*transitioncost.DirectedTableRecord)
	}{
		{name: "summary method", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.SummaryMethod = "retired"
		}},
		{name: "entry count", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries = record.Entries[:1]
		}},
		{name: "reverse direction", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[1].TargetState = record.Entries[0].TargetState
		}},
		{name: "baseline target", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].TargetIdleBaseline.State = record.Entries[0].SourceState
		}},
		{name: "verdict counts", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].VerdictCounts.Invalid++
		}},
		{name: "accepted count", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].AcceptedCount = 4
		}},
		{name: "survival counts", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].ServingSurvivedTrueCount++
		}},
		{name: "non-finite distribution", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].EtaJoules.Mean = math.NaN()
		}},
		{name: "unordered distribution", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].TauMilliseconds.Minimum = record.Entries[0].TauMilliseconds.Maximum + 1
		}},
		{name: "non-positive tau", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].TauMilliseconds.Minimum = 0
		}},
		{name: "negative uncertainty", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].MaximumBoundaryMappingUncertaintyMilliseconds = -1
		}},
		{name: "excessive uncertainty", mutate: func(record *transitioncost.DirectedTableRecord) {
			record.Entries[0].MaximumBoundaryMappingUncertaintyMilliseconds = math.MaxInt64
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := completeDirectedTableRecord(t)
			test.mutate(&record)
			if _, err := transitioncost.RestoreDirectedTable(record); err == nil {
				t.Fatal("RestoreDirectedTable() unexpectedly succeeded")
			}
		})
	}
}

func completeDirectedTableRecord(t *testing.T) transitioncost.DirectedTableRecord {
	t.Helper()
	directions := testDirections()
	table, err := transitioncost.Summarize(directions, 5, completeResults(t, directions))
	if err != nil {
		t.Fatal(err)
	}
	return table.Record()
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
) []transitioncost.FinalizedObservation {
	t.Helper()
	results := make([]transitioncost.FinalizedObservation, 0, 16)
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
					raw.MarkerBrackets.ApplyStartedAt.LastAbsentReceivedAt =
						raw.MarkerBrackets.ApplyStartedAt.FirstPresentReceivedAt.
							Add(-time.Duration(index+1) * time.Millisecond)
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
) transitioncost.FinalizedObservation {
	t.Helper()
	frozen, raw, policy := validInputs()
	frozen.SourceState = direction.Source
	frozen.TargetState = direction.Target
	frozen.TargetIdleBaseline = baselineFor(direction.Target)

	startedAt := testTime(10)
	completedAt := startedAt.Add(time.Duration(tauSeconds) * time.Second)
	raw.ObservationStartedAt = startedAt.Add(-2 * time.Second)
	raw.ObservationEndedAt = completedAt.Add(2 * time.Second)
	raw.SourceReadBack = transitioncost.StateReadBack{
		Fact: transitioncost.MappedFact{
			OccurredAt: startedAt.Add(-time.Second),
			MappedAt:   startedAt.Add(-time.Second),
		},
		State: direction.Source,
	}
	raw.TransitionStart = &transitioncost.OccurredFact{OccurredAt: startedAt}
	raw.TargetReadBack = &transitioncost.TargetStateReadBack{
		Fact:  transitioncost.OccurredFact{OccurredAt: startedAt.Add(time.Second)},
		State: direction.Target,
	}
	raw.MarkerBrackets = transitioncost.TransitionMarkerBrackets{
		ApplyStartedAt: markerBracket(startedAt, time.Millisecond),
		TargetObservation: markerBracket(
			startedAt.Add(time.Second),
			time.Millisecond,
		),
		ReadyProcessWitness: markerBracket(completedAt, time.Millisecond),
	}
	raw.Readiness = []transitioncost.ReadyObservation{{
		ObservedAt: completedAt, Ready: true,
	}}
	raw.PreTransitionProcess.ObservedAt = startedAt.Add(-time.Second)
	if !servingSurvived {
		raw.PostTransitionProcess.ContainerID = "replacement-container"
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
		raw.PostTransitionProcess = nil
		raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
	case insufficientAttempt:
		raw.MarkerBrackets.ApplyStartedAt.LastAbsentReceivedAt =
			startedAt.Add(-policy.MaximumBoundaryUncertainty - time.Millisecond)
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
	finalized, err := transitioncost.FinalizeResult(result)
	if err != nil {
		t.Fatalf("finalize attempt %q: %v", id, err)
	}
	return finalized
}

func markerBracket(firstPresent time.Time, width time.Duration) transitioncost.ReceiptTimeBracket {
	return transitioncost.ReceiptTimeBracket{
		LastAbsentReceivedAt:   firstPresent.Add(-width),
		FirstPresentReceivedAt: firstPresent,
	}
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
