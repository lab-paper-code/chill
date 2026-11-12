package firstfloor

import (
	"context"
	"testing"
	"time"
)

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
		if unknown.Size != 1 || unknown.RequestID != request.Context.RequestID || unknown.Reason != ReasonMissingConditionalSupport {
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

func TestActorMissingStartupSupportDoesNotExpireRejectAsInfeasible(t *testing.T) {
	s, cfg := startTestActor(t, modeServing, "0", func(cfg *Config) {
		cfg.Policy.MaxBatchSize = 1
		cfg.Policy.MaxBatchWaitMS = 0
		cfg.Policy.Batches = []BatchProfile{testBatch(1, 1, pairedSample("known-gap-only", 1, 10))}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := actorRequest(t, cfg, "missing-startup", "workload", 1)
	request.context.AdmissionPolicy = AdmissionReject
	request.context.SenderElapsedMS = 2
	reply := s.submit(ctx, request)
	if reply.err != nil || reply.response.Progress.BatchID == 0 || reply.response.Progress.Outcome != outcomeCompleted {
		t.Fatalf("actor rejected an unknown startup quantile before ChooseBatch could execute: %+v", reply)
	}
	// The completed exchange supplies a known gap for the next request. The
	// same expired SLO must then reject without a second physical execution.
	known := actorRequest(t, cfg, "known-gap", "workload", 1)
	known.context.AdmissionPolicy = AdmissionReject
	known.context.SenderElapsedMS = 2
	reply = s.submit(ctx, known)
	if reply.err != ErrDeadlineInfeasible || reply.response.Progress.BatchID != 0 || !reply.complete {
		t.Fatalf("known support lost the actor's pre-execution rejection: %+v", reply)
	}
}
