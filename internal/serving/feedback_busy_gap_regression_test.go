package serving

import (
	"math"
	"testing"
	"time"
)

func TestFeedbackConsumerBusyPotentialUsesNextBatchGap(t *testing.T) {
	potentials := make([]float64, 0, 2)
	for _, service := range []struct {
		busy int64
		idle int64
	}{{10e6, 40e6}, {40e6, 10e6}} {
		settings, snapshot, candidate := feedbackFixture()
		snapshot.Arrivals = nil
		snapshot.Instances[0].MaxBatch = 1
		snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
		snapshot.Workloads[0].Admission.Policy = AdmissionPolicyDelay
		// One 59-record window per gap: the running batch followed a 1s gap
		// (30ms law: 9ms residual after 20ms+1ms), its successor follows it
		// with no gap (busy law), and the actor's last idle age is 100ms.
		busy, idle, prior := int64(0), int64(100e6), int64(1e9)
		snapshot.Instances[0].LastServiceEndAgeNanos = &idle
		snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestPairedBatch(BatchProfile{Batch: 1}, schedulerTestGapLaws([2]int64{prior, 30e6}, [2]int64{busy, service.busy}, [2]int64{idle, service.idle}))}
		post, sender, native := 0.0, 0.0, 20e6
		elapsed := int64(20e6)
		life := time.Millisecond
		snapshot.Outstanding = []SchedulerRequest{
			{ID: "running", Definition: snapshot.Workloads[0], Phase: phaseRunning, Instance: "i1", BatchID: "actual", Elapsed: 20 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native, PrecedingIdleNanos: &prior, ExchangeElapsedNanos: &elapsed},
			{ID: "waiting", Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &sender, RemainingLifetime: &life},
		}
		result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
		score := result.Workloads[0]
		want := float64(service.busy) / 10e6
		if score.PotentialExcess == nil {
			t.Fatalf("expired queued request lost conditional potential: %+v", result)
		}
		if score.Timeout != 1 || score.OnTime+score.Late != 1 || math.Abs(*score.PotentialExcess-want) > 1e-12 {
			t.Fatalf("potential must use 1ms age + 9ms residual + busy successor service, not last idle gap: want=%g actual=%g result=%+v", want, *score.PotentialExcess, result)
		}
		potentials = append(potentials, *score.PotentialExcess)
	}
	if potentials[0] >= potentials[1] {
		t.Fatalf("faster busy successor must improve failure potential despite slower idle successor: %v", potentials)
	}
}
