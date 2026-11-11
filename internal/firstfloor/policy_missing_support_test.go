package firstfloor

import "testing"

func TestChooseBatchMissingConditionalSupportExecutesWithoutRejection(t *testing.T) {
	gap := int64(0)
	distribution, err := NewServiceDistribution([]ServiceSample{{ServiceDurationNanos: 10000000, PrecedingIdleNanos: &gap, TrialID: "known-gap-only"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{MaxBatchSize: 1, Batches: []BatchProfile{{Size: 1, Distribution: distribution}}}
	for _, admission := range []string{AdmissionReject, AdmissionDelay} {
		request := RequestState{Context: RequestContext{RequestID: "request", WorkloadUID: "workload", Definition: "frozen", AdmissionPolicy: admission, SLO: SLO{Percentile: 95, TargetMS: 1}}, Phase: phaseQueued, ElapsedMS: 2}
		unknown := ChooseBatch(policy, []RequestState{request}, nil)
		if unknown.Size != 1 || unknown.RequestID != request.Context.RequestID || unknown.Reason != "missing_conditional_support" {
			t.Fatalf("%s rejected or deferred on absent startup evidence: %+v", admission, unknown)
		}
		known := ChooseBatch(policy, []RequestState{request}, &gap)
		want := OutcomeDeadlineDrop
		if admission == AdmissionDelay {
			want = ReasonDeadlineDefer
		}
		if known.Size != 0 || known.Reason != want {
			t.Fatalf("%s lost feasibility enforcement with known support: %+v", admission, known)
		}
	}
}
