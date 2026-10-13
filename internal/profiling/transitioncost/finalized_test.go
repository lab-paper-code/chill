package transitioncost_test

import (
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestFinalizedObservationProjectsAndRestoresEveryVerdict(t *testing.T) {
	direction := testDirections()[0]
	for _, outcome := range []attemptOutcome{acceptedAttempt, invalidAttempt, failedAttempt, insufficientAttempt} {
		t.Run(map[attemptOutcome]string{
			acceptedAttempt: "accepted", invalidAttempt: "invalid",
			failedAttempt: "failed", insufficientAttempt: "insufficient",
		}[outcome], func(t *testing.T) {
			finalized := deriveAttempt(t, direction, "finalized", 3, 2, outcome, nil)
			record := finalized.Record()
			restored, err := transitioncost.RestoreFinalizedObservation(record)
			if err != nil {
				t.Fatal(err)
			}
			if restored.FrozenContract() != finalized.FrozenContract() ||
				restored.Verdict() != finalized.Verdict() || restored.Reason() != finalized.Reason() ||
				restored.Detail() != finalized.Detail() {
				t.Fatalf("restored finalized observation = %#v", restored.Record())
			}
			accepted, acceptedOK := restored.AcceptedObservation()
			if acceptedOK != (outcome == acceptedAttempt) {
				t.Fatalf("accepted observation present = %t", acceptedOK)
			}
			if acceptedOK && (accepted.TauMilliseconds() != 3000 || accepted.EtaJoules() != 2 ||
				accepted.TargetIdleBaseline() != directionBaseline(direction)) {
				t.Fatalf("restored accepted observation = %#v", record.AcceptedObservation)
			}
		})
	}
}

func TestRestoreFinalizedObservationRejectsMalformedRecord(t *testing.T) {
	valid := deriveAttempt(t, testDirections()[0], "valid", 3, 2, acceptedAttempt, nil).Record()
	tests := []struct {
		name   string
		mutate func(*transitioncost.FinalizedObservationRecord)
	}{
		{name: "unknown verdict", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.Verdict = "Other"
		}},
		{name: "accepted without value", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.AcceptedObservation = nil
		}},
		{name: "accepted reason mismatch", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.Reason = transitioncost.ReasonPowerWindowInsufficient
		}},
		{name: "invalid window", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.AcceptedObservation.CompletedAt = record.AcceptedObservation.StartedAt
		}},
		{name: "non-finite eta", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.AcceptedObservation.EtaJoules = math.Inf(1)
		}},
		{name: "negative uncertainty", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.AcceptedObservation.BoundaryMappingUncertaintyMilliseconds = -1
		}},
		{name: "nonaccepted with value", mutate: func(record *transitioncost.FinalizedObservationRecord) {
			record.Verdict = transitioncost.VerdictFailed
			record.Reason = transitioncost.ReasonReadinessNotReached
			record.Detail = "readiness missing"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			accepted := *valid.AcceptedObservation
			record.AcceptedObservation = &accepted
			test.mutate(&record)
			if _, err := transitioncost.RestoreFinalizedObservation(record); err == nil {
				t.Fatal("malformed finalized observation was accepted")
			}
		})
	}
}

func TestFinalizedObservationRecordDoesNotAliasAcceptedValues(t *testing.T) {
	finalized := deriveAttempt(t, testDirections()[0], "owned", 3, 2, acceptedAttempt, nil)
	record := finalized.Record()
	record.AcceptedObservation.StartedAt = time.Time{}
	if got := finalized.Record().AcceptedObservation.StartedAt; got.IsZero() {
		t.Fatal("finalized observation aliases its returned record")
	}
}

func directionBaseline(direction transitioncost.DirectedStatePair) transitioncost.IdleBaseline {
	return baselineFor(direction.Target)
}
