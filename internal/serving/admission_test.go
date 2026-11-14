package serving

import (
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func admissionTestPolicy() firstfloor.Policy {
	one := schedulerTestBatchProfile(BatchProfile{Batch: 1, InputSlopeJoules: schedulerTestSlope(2)}, []float64{float64(20 * time.Millisecond)})
	two := schedulerTestBatchProfile(BatchProfile{Batch: 2, InputSlopeJoules: schedulerTestSlope(1)}, []float64{float64(100 * time.Millisecond), float64(200 * time.Millisecond)})
	return firstfloor.Policy{ProfileID: "profile", MaxBatchSize: 2, Batches: (&Profile{ID: "profile", Batches: []BatchProfile{one, two}}).NativeBundle().Batches}
}

// The 95th percentile's 59-record window keeps survivors for a running
// batch's residual; a 50th percentile window holds one record.
func admissionTestRequest(id string) firstfloor.RequestState {
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: id, WorkloadUID: "w", Definition: "d", AdmissionPolicy: "Reject", SLO: firstfloor.SLO{Percentile: 95, TargetMS: 1000}}, Phase: phaseQueued}
}

func TestAdmissionCompletionIncludesResidualOnceAndSnapshotAge(t *testing.T) {
	p := admissionTestPolicy()
	elapsed, gap := int64(150*time.Millisecond), int64(0)
	a, b := admissionTestRequest("running-a"), admissionTestRequest("running-b")
	for _, q := range []*firstfloor.RequestState{&a, &b} {
		q.Phase, q.BatchID, q.BatchSize = phaseRunning, 7, 2
		q.ExchangeElapsedNanos, q.PrecedingIdleNanos = &elapsed, &gap
	}
	for _, age := range []time.Duration{0, 10 * time.Millisecond, 60 * time.Millisecond} {
		got := EstimateAdmission(AdmissionInput{Policy: p, Observed: []firstfloor.RequestState{a, b}, SnapshotAge: age, Candidate: admissionTestRequest("candidate")})
		want := max(50*time.Millisecond-age, 0) + 20*time.Millisecond
		if !got.Known || got.Completion != want {
			t.Fatalf("age %v completion=%+v, want %v", age, got, want)
		}
	}
}

func TestAdmissionUnknownMissingInputsAndExhaustedResidual(t *testing.T) {
	for _, name := range []string{"distribution", "policy", "context", "percentile", "fill policy", "exchange", "residual"} {
		t.Run(name, func(t *testing.T) {
			in := AdmissionInput{Policy: admissionTestPolicy(), Candidate: admissionTestRequest("candidate")}
			switch name {
			case "distribution":
				in.Policy.Batches[0].Distribution = nil
			case "policy":
				in.Candidate.Context.AdmissionPolicy = ""
			case "context":
				in.Candidate.Context.Definition = ""
			case "percentile":
				in.Candidate.Context.SLO.Percentile = 0
			case "fill policy":
				in.Policy.MaxBatchWaitMS = -1
			default:
				q := admissionTestRequest("running")
				q.Phase, q.BatchID, q.BatchSize = phaseRunning, 1, 2
				elapsed, gap := int64(200*time.Millisecond), int64(0)
				q.PrecedingIdleNanos = &gap
				if name == "residual" {
					q.ExchangeElapsedNanos = &elapsed
				}
				in.Observed = []firstfloor.RequestState{q}
			}
			if got := EstimateAdmission(in); got.Known || got.FixedBatch {
				t.Fatalf("missing input became judgment: %+v", got)
			}
		})
	}
	if got := EstimateAdmission(AdmissionInput{Policy: firstfloor.Policy{FixedBatchSize: 16}}); !got.FixedBatch || got.Known {
		t.Fatalf("fixed batch acquired SLO judgment: %+v", got)
	}
}

func TestAdmissionUsesNormalEDFBeforeDeferredAndAssignment(t *testing.T) {
	candidate := admissionTestRequest("candidate")
	candidate.Phase = phaseDeferred
	candidate.Context.CompletionMarginMS = 3
	first := admissionTestRequest("normal")
	first.Context.WorkloadUID = schedulerTestOther
	assigned := admissionTestRequest("unobserved")
	assigned.Context.WorkloadUID = "third"
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), LastServiceEndAgeNanos: new(int64), Observed: []firstfloor.RequestState{first}, Assignments: []firstfloor.RequestState{assigned}, Candidate: candidate})
	if !got.Known || got.Completion != 63*time.Millisecond {
		t.Fatalf("normal/assignment precedence or reserve lost: %+v", got)
	}
}

func TestAdmissionRejectCandidateStillPredictsInfeasibleCompletion(t *testing.T) {
	candidate := admissionTestRequest("candidate")
	candidate.Context.SLO.TargetMS = 10
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), LastServiceEndAgeNanos: new(int64), Candidate: candidate})
	if !got.Known || got.Completion != 20*time.Millisecond {
		t.Fatalf("infeasible candidate lost predicted completion: %+v", got)
	}
}

func TestAdmissionSharedFillWaitIsBounded(t *testing.T) {
	p := admissionTestPolicy()
	p.MaxBatchWaitMS = 20
	// A lower-energy larger batch invites fill, but no future arrival is invented.
	p.Batches[1].Distribution = p.Batches[0].Distribution
	candidate := admissionTestRequest("candidate")
	candidate.ArrivalRate = 100
	got := EstimateAdmission(AdmissionInput{Policy: p, LastServiceEndAgeNanos: new(int64), Candidate: candidate})
	if !got.Known || got.Completion != 40*time.Millisecond {
		t.Fatalf("fill wait did not consume fixed budget: %+v", got)
	}
}

func TestAdmissionPreservesNearestGapOutsideSupportEvidence(t *testing.T) {
	batch := schedulerTestPairedBatch(BatchProfile{Batch: 1}, schedulerTestGapLaws([2]int64{int64(time.Second), int64(20 * time.Millisecond)}))
	p := firstfloor.Policy{MaxBatchSize: 1, Batches: (&Profile{Batches: []BatchProfile{batch}}).NativeBundle().Batches}
	idle := int64(0)
	got := EstimateAdmission(AdmissionInput{Policy: p, LastServiceEndAgeNanos: &idle, Candidate: admissionTestRequest("candidate")})
	if !got.Known || !got.OutsideSupport || got.Completion != 20*time.Millisecond {
		t.Fatalf("nearest-gap support evidence lost: %+v", got)
	}
}

func TestAdmissionProjectionExpiresLifetimeWithoutMutatingSnapshot(t *testing.T) {
	for _, phase := range []string{phaseQueued, phaseDeferred, "assigned"} {
		t.Run(phase, func(t *testing.T) {
			running := admissionTestRequest("running")
			running.Phase, running.BatchID, running.BatchSize = phaseRunning, 1, 1
			elapsed := int64(0)
			running.ExchangeElapsedNanos, running.PrecedingIdleNanos = &elapsed, new(int64)
			remaining := float64(5)
			queued := admissionTestRequest("expires")
			queued.Context.SLO.TargetMS = 100
			queued.RemainingLifetimeMS = &remaining
			queued.Phase = phase
			in := AdmissionInput{Policy: admissionTestPolicy(), Observed: []firstfloor.RequestState{running}, Candidate: admissionTestRequest("candidate")}
			if phase == phaseDeferred {
				in.Candidate.Phase = phaseDeferred
			}
			if phase == "assigned" {
				queued.Phase = phaseQueued
				in.Assignments = []firstfloor.RequestState{queued}
			} else {
				in.Observed = append(in.Observed, queued)
			}
			got := EstimateAdmission(in)
			if !got.Known || got.Completion != 40*time.Millisecond || remaining != 5 {
				t.Fatalf("expired queue retained or snapshot lifetime mutated: %+v remaining=%v", got, remaining)
			}
		})
	}
}

func TestAdmissionProjectionKeepsLifetimeExpiredRunningOccupancy(t *testing.T) {
	running := admissionTestRequest("running")
	running.Phase, running.BatchID, running.BatchSize = phaseRunning, 1, 1
	elapsed := int64(0)
	remaining := float64(0)
	running.ExchangeElapsedNanos, running.PrecedingIdleNanos = &elapsed, new(int64)
	running.RemainingLifetimeMS = &remaining
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), Observed: []firstfloor.RequestState{running}, Candidate: admissionTestRequest("candidate")})
	if !got.Known || got.Completion != 40*time.Millisecond {
		t.Fatalf("HTTP lifetime released physical running occupancy: %+v", got)
	}
}

func TestAdmissionCandidateLifetimeMissIsKnownInfeasible(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("waiting=%t", waiting), func(t *testing.T) {
			candidate := admissionTestRequest("candidate")
			lifetime := float64(5)
			candidate.RemainingLifetimeMS = &lifetime
			in := AdmissionInput{Policy: admissionTestPolicy(), LastServiceEndAgeNanos: new(int64), Candidate: candidate}
			if waiting {
				running := admissionTestRequest("running")
				running.Phase, running.BatchID, running.BatchSize = phaseRunning, 1, 1
				elapsed := int64(0)
				running.ExchangeElapsedNanos, running.PrecedingIdleNanos = &elapsed, new(int64)
				in.Observed = []firstfloor.RequestState{running}
			}
			got := EstimateAdmission(in)
			if !got.Known || !got.Infeasible || lifetime != 5 {
				t.Fatalf("known lifetime miss became Unknown or mutated input: %+v", got)
			}
			if waiting && got.Completion != 0 {
				t.Fatalf("expired queue request acquired executable completion: %+v", got)
			}
			if !waiting && got.Completion != 20*time.Millisecond {
				t.Fatalf("selected batch completion prediction lost: %+v", got)
			}
		})
	}
}

func TestAdmissionCompletionAtLifetimeIsInfeasible(t *testing.T) {
	candidate := admissionTestRequest("candidate")
	lifetime := float64(20)
	candidate.RemainingLifetimeMS = &lifetime
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), LastServiceEndAgeNanos: new(int64), Candidate: candidate})
	if !got.Known || !got.Infeasible || got.Completion != 20*time.Millisecond || lifetime != 20 {
		t.Fatalf("completion at native timeout must be infeasible without mutating lifetime: %+v remaining=%v", got, lifetime)
	}
}

func TestAdmissionFillWakesAtEarliestQueuedLifetime(t *testing.T) {
	p := admissionTestPolicy()
	p.MaxBatchWaitMS = 100
	p.Batches[1].Distribution = p.Batches[0].Distribution
	expiring := admissionTestRequest("expires")
	expiring.Context.WorkloadUID = schedulerTestOther
	expiring.Context.SLO.TargetMS = 50
	expiring.ArrivalRate = 100
	remaining := float64(5)
	expiring.RemainingLifetimeMS = &remaining
	got := EstimateAdmission(AdmissionInput{Policy: p, LastServiceEndAgeNanos: new(int64), Observed: []firstfloor.RequestState{expiring}, Candidate: admissionTestRequest("candidate")})
	if !got.Known || got.Infeasible || got.Completion != 25*time.Millisecond || remaining != 5 {
		t.Fatalf("fill skipped earlier native lifetime wake: %+v remaining=%v", got, remaining)
	}
}

func TestAdmissionExpiryCompactsWithoutReordering(t *testing.T) {
	for _, expiredID := range []string{"first", "candidate", "last"} {
		t.Run(expiredID, func(t *testing.T) {
			states := []firstfloor.RequestState{admissionTestRequest("first"), admissionTestRequest("candidate"), admissionTestRequest("last")}
			for j := range states {
				if states[j].Context.RequestID == expiredID {
					states[j].RemainingLifetimeMS = new(float64)
				}
			}
			kept, expired := admissionExpire(states, "candidate")
			if len(kept) != 2 || expired != (expiredID == "candidate") {
				t.Fatalf("expiry result: states=%+v candidateExpired=%t", kept, expired)
			}
			next := 0
			for _, id := range []string{"first", "candidate", "last"} {
				if id != expiredID {
					if kept[next].Context.RequestID != id {
						t.Fatalf("expiry reordered survivors: %+v", kept)
					}
					next++
				}
			}
			if states[2].Context.RequestID != "" || states[2].RemainingLifetimeMS != nil {
				t.Fatalf("expiry retained removed state: %+v", states[2])
			}
		})
	}
}

func BenchmarkAdmissionProjectionBacklog(b *testing.B) {
	for _, queued := range []int{0, 16, 32} {
		b.Run(fmt.Sprint(queued), func(b *testing.B) {
			in := AdmissionInput{Policy: admissionTestPolicy(), Candidate: admissionTestRequest("candidate")}
			in.Policy.MaxBatchSize = 1
			for j := range queued {
				in.Observed = append(in.Observed, admissionTestRequest(fmt.Sprint(j)))
			}
			var scratch admissionScratch
			var got AdmissionEstimate
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got = projectAdmission(in, &scratch)
			}
			b.StopTimer()
			if !got.Known || got.Infeasible || got.Completion != time.Duration(queued+1)*20*time.Millisecond {
				b.Fatalf("backlog completion: %+v", got)
			}
		})
	}
}
