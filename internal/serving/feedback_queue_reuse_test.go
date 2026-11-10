package serving

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestFeedbackStableQueueCountMatchesLiveScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	identities := []string{"a", "b", "c"}
	for range 200 {
		instance := &schedulerReplayInstance{}
		for range 40 {
			request := &schedulerReplayRequest{identity: identities[rng.IntN(len(identities))], deferred: rng.IntN(2) == 0, expired: rng.IntN(5) == 0, dropped: rng.IntN(5) == 0, dispatched: true}
			if rng.IntN(2) == 0 {
				request.source.Phase = phaseAssigned
			}
			instance.queue = append(instance.queue, request)
		}
		state := schedulerReplayState{finalizingFeedback: true}
		queries := append([]*schedulerReplayRequest(nil), instance.queue...)
		for _, identity := range identities {
			for _, deferred := range []bool{false, true} {
				for _, phase := range []string{phasePending, phaseAssigned, phaseQueued} {
					queries = append(queries, &schedulerReplayRequest{identity: identity, deferred: deferred, dispatched: phase != phasePending, source: SchedulerRequest{Phase: phase}})
				}
			}
		}
		for _, request := range queries {
			if got, want := state.potentialBatchCount(instance, request), potentialBatchCount(instance, request); got != want {
				t.Fatalf("stable summary changed membership/deferred FIFO: request=%+v got=%d want=%d", request, got, want)
			}
		}
		if len(state.potentialQueues) != 1 {
			t.Fatalf("queue was not reused: %d", len(state.potentialQueues))
		}
	}
}

func TestFeedbackExpiryPotentialReadsEachQueueMutation(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
	snapshot.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDrop
	snapshot.Instances[0].Profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{60e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2}, []float64{1e6}),
	}
	zero, life := 0.0, time.Duration(0)
	for _, id := range []string{"a", "b"} {
		snapshot.Outstanding = append(snapshot.Outstanding, SchedulerRequest{ID: id, Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero, RemainingLifetime: &life})
	}
	state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	state.initializeInstances()
	state.initializeOutstanding()
	state.admitArrivals()
	state.expireRequests()
	if !state.requests[0].hasPotential || state.requests[0].potential != 0 || !state.requests[1].hasPotential || state.requests[1].potential != 5 || state.out.conditional.Queries != 3 || state.potentialQueues != nil {
		t.Fatalf("same-time expiry froze queue state: first=%+v second=%+v evidence=%+v", state.requests[0], state.requests[1], state.out.conditional)
	}
}

func TestFeedbackPotentialCompatibilityUsesImmutablePointers(t *testing.T) {
	_, snapshot, _ := feedbackFixture()
	definition, profile := snapshot.Workloads[0], snapshot.Instances[0].Profile
	state := schedulerReplayState{}
	for range 3 {
		if !state.potentialProfileMatches(definition, profile) {
			t.Fatal("compatible profile rejected")
		}
	}
	other := *profile
	other.Condition.LogicalModel += "-other"
	if state.potentialProfileMatches(definition, &other) || state.potentialProfileMatches(definition, &other) {
		t.Fatal("incompatible profile accepted")
	}
	frozen := *definition
	frozen.Generation++
	if !state.potentialProfileMatches(&frozen, profile) || len(state.potentialProfiles) != 3 {
		t.Fatalf("compatibility leaked across profiles/frozen definitions: %+v", state.potentialProfiles)
	}
}
