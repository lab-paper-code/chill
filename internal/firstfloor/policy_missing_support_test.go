package firstfloor

import (
	"testing"
)

// One record is below the p95 sample floor (59): the quantile is not
// identified and the request executes; with the floor covered the expired
// SLO is enforced.
func TestChooseBatchMissingConditionalSupportExecutesWithoutRejection(t *testing.T) {
	gap := int64(0)
	sample := ServiceSample{ServiceDurationNanos: 10000000, PrecedingIdleNanos: &gap, TrialID: "known-gap-only"}
	sparse, err := NewServiceDistribution([]ServiceSample{sample})
	if err != nil {
		t.Fatal(err)
	}
	for _, admission := range []string{AdmissionReject, AdmissionDelay} {
		request := RequestState{Context: RequestContext{RequestID: "request", WorkloadUID: "workload", Definition: "frozen", AdmissionPolicy: admission, SLO: SLO{Percentile: 95, TargetMS: 1}}, Phase: phaseQueued, ElapsedMS: 2}
		unknown := ChooseBatch(Policy{MaxBatchSize: 1, Batches: []BatchProfile{{Size: 1, Distribution: sparse}}}, []RequestState{request}, &gap)
		if unknown.Size != 1 || unknown.RequestID != request.Context.RequestID || unknown.Reason != ReasonMissingConditionalSupport {
			t.Fatalf("%s rejected or deferred on unidentified service: %+v", admission, unknown)
		}
		known := ChooseBatch(Policy{MaxBatchSize: 1, Batches: []BatchProfile{testBatch(1, 0, sample)}}, []RequestState{request}, &gap)
		want := OutcomeDeadlineDrop
		if admission == AdmissionDelay {
			want = ReasonDeadlineDefer
		}
		if known.Size != 0 || known.Reason != want {
			t.Fatalf("%s lost feasibility enforcement with known support: %+v", admission, known)
		}
	}
}
