package serving

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func admissionTestPolicy() firstfloor.Policy {
	one := schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 2}, []float64{float64(20 * time.Millisecond)})
	two := schedulerTestBatchProfile(BatchProfile{Batch: 2, EnergyPerInputJoules: 1}, []float64{float64(100 * time.Millisecond), float64(200 * time.Millisecond)})
	return firstfloor.Policy{ProfileID: "profile", MaxBatchSize: 2, Batches: (&Profile{ID: "profile", Batches: []BatchProfile{one, two}}).NativeBundle().Batches}
}

func admissionTestRequest(id string) firstfloor.RequestState {
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: id, WorkloadUID: "w", Definition: "d", AdmissionPolicy: "Reject", SLO: firstfloor.SLO{Percentile: 50, TargetMS: 1000}}, Phase: phaseQueued}
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
	first.Context.WorkloadUID = "other"
	assigned := admissionTestRequest("unobserved")
	assigned.Context.WorkloadUID = "third"
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), Observed: []firstfloor.RequestState{first}, Assignments: []firstfloor.RequestState{assigned}, Candidate: candidate})
	if !got.Known || got.Completion != 63*time.Millisecond {
		t.Fatalf("normal/assignment precedence or reserve lost: %+v", got)
	}
}

func TestAdmissionRejectCandidateStillPredictsInfeasibleCompletion(t *testing.T) {
	candidate := admissionTestRequest("candidate")
	candidate.Context.SLO.TargetMS = 10
	got := EstimateAdmission(AdmissionInput{Policy: admissionTestPolicy(), Candidate: candidate})
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
	got := EstimateAdmission(AdmissionInput{Policy: p, Candidate: candidate})
	if !got.Known || got.Completion != 40*time.Millisecond {
		t.Fatalf("fill wait did not consume fixed budget: %+v", got)
	}
}

func TestAdmissionPreservesNearestGapOutsideSupportEvidence(t *testing.T) {
	gap := int64(time.Second)
	batch := schedulerTestPairedBatch(BatchProfile{Batch: 1}, []firstfloor.ServiceSample{
		{TrialID: "physical", ServiceDurationNanos: int64(20 * time.Millisecond), PrecedingIdleNanos: &gap},
	})
	p := firstfloor.Policy{MaxBatchSize: 1, Batches: (&Profile{Batches: []BatchProfile{batch}}).NativeBundle().Batches}
	idle := int64(0)
	got := EstimateAdmission(AdmissionInput{Policy: p, LastServiceEndAgeNanos: &idle, Candidate: admissionTestRequest("candidate")})
	if !got.Known || !got.OutsideSupport || got.Completion != 20*time.Millisecond {
		t.Fatalf("nearest-gap support evidence lost: %+v", got)
	}
}
