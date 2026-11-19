package serving

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestReplayRestoresDerivedNativeSpanAnchorAndGapBounds(t *testing.T) {
	s := replayAdmissionState(t)
	observed := s.snap.At
	anchor := observed.Add(100 * time.Millisecond)
	lower, upper := int64(0), int64(30*time.Millisecond)
	profile := s.snap.Instances[0].Profile
	i := &s.snap.Instances[0]
	i.ObservedAt, i.NativeSpanAt = observed, anchor
	i.LastServiceEndAgeNanos, i.LastServiceEndAgeUpperNanos = &lower, &upper
	i.ArrivalAgesMS = map[string][]float64{"w1": {3, 7}}
	s.snap.At, s.snap.ObservationEnd = anchor.Add(10*time.Millisecond), anchor.Add(10*time.Millisecond)
	data, err := json.Marshal(s.snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s.snap); err != nil {
		t.Fatal(err)
	}
	s.snap.Instances[0].Profile = profile
	s.initializeInstances()
	executor := s.instances[0]
	if got := executor.idleNanos(0); got == nil || *got != int64(10*time.Millisecond) {
		t.Fatalf("replay counted the original observation age twice: %v", got)
	}
	if got := executor.idleUpperNanos(0); got == nil || *got != int64(40*time.Millisecond) {
		t.Fatalf("replay changed service-end bounds: %v", got)
	}
	history := executor.arrivalTimes["w1"]
	if len(history) != 2 || history[0] != -float64(13*time.Millisecond) || history[1] != -float64(17*time.Millisecond) {
		t.Fatalf("replay double-counted native arrival age: %v", history)
	}
	if !executor.config.ObservedAt.Equal(observed) {
		t.Fatal("replay changed physical observation provenance")
	}
}

func TestReplayUsesCompletionGapEnvelopeUntilNextServiceEnd(t *testing.T) {
	s := replayAdmissionState(t)
	zero, thirty := int64(0), int64(30*time.Millisecond)
	profile := *s.snap.Instances[0].Profile
	profile.Batches = []BatchProfile{schedulerTestPairedBatch(BatchProfile{Batch: 1}, schedulerTestGapLaws([2]int64{zero, int64(20 * time.Millisecond)}, [2]int64{thirty, int64(50 * time.Millisecond)}))}
	config := &s.snap.Instances[0]
	config.Profile, config.MaxBatch = &profile, 1
	config.NativeSpanAt = s.snap.At
	config.LastServiceEndAgeNanos, config.LastServiceEndAgeUpperNanos = &zero, &thirty
	s.initializeInstances()
	i := s.instances[0]
	definition := s.snap.Workloads[0]
	native := replayAdmissionPending(s, "native", definition)
	native.dispatched, native.source.Instance, native.native = true, i.config.ID, math.Inf(1)
	i.queue, i.outstanding = []*schedulerReplayRequest{native}, 1
	if !s.chooseBatches(context.Background()) || i.nativeFinish != float64(50*time.Millisecond) || !i.hasBatchIdleBounds {
		t.Fatalf("replay sampled an optimistic initial gap: finish=%g bounds=%v", i.nativeFinish, i.hasBatchIdleBounds)
	}
	s.now = i.finish
	s.completeExecution()
	if i.idleUpperNanos(s.now) != nil || i.runningIdleUpper() != nil {
		t.Fatal("completed simulated service retained old gap uncertainty")
	}
}

func TestReplayQueuedExecutionDoesNotGuessUnknownIdleGap(t *testing.T) {
	s := replayAdmissionState(t)
	s.snap.Instances[0].AdmissionIdleUnknown = true
	s.initializeInstances()
	i := s.instances[0]
	native := replayAdmissionPending(s, "native", s.snap.Workloads[0])
	native.dispatched, native.source.Instance, native.native = true, i.config.ID, math.Inf(1)
	i.queue = []*schedulerReplayRequest{native}
	if s.chooseBatches(context.Background()) {
		t.Fatal("replay guessed a service gap despite unknown physical history")
	}
}
