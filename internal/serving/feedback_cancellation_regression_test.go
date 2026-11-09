package serving

import (
	"context"
	"math/rand/v2"
	"testing"
)

type feedbackCancelDuringReplay struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *feedbackCancelDuringReplay) Err() error {
	c.calls++
	if c.calls == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFeedbackConsumerCanceledReplaySkipsCohortAggregation(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	zero := 0.0
	snapshot.Outstanding = []SchedulerRequest{{ID: "accepted", Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero}}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &feedbackCancelDuringReplay{Context: base, cancel: cancel}
	result, err := schedulerReplay(ctx, settings, snapshot, candidate, rand.New(rand.NewPCG(1, 0)), map[NodeReference]bool{}, nil)
	if err != context.Canceled || len(result.feedback) != 0 || result.conditional.Queries != 0 || len(result.outcomes) != 0 {
		t.Fatalf("canceled replay must not reduce accepted cohort or query failure potentials: %+v %v", result, err)
	}
}
