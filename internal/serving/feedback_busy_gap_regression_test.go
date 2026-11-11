package serving

import (
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
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
		busy, idle := int64(0), int64(100e6)
		snapshot.Instances[0].LastServiceEndAgeNanos = &idle
		// Conditional gap selection happens within each physical trial.
		snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestPairedBatch(BatchProfile{Batch: 1}, []firstfloor.ServiceSample{
			{ServiceDurationNanos: 30e6, TrialID: "physical-a"},
			{ServiceDurationNanos: service.busy, PrecedingIdleNanos: &busy, TrialID: "physical-a"},
			{ServiceDurationNanos: service.idle, PrecedingIdleNanos: &idle, TrialID: "physical-a"},
		})}
		post, sender, native := 0.0, 0.0, 20e6
		elapsed := int64(20e6)
		life := time.Millisecond
		snapshot.Outstanding = []SchedulerRequest{
			{ID: "running", Definition: snapshot.Workloads[0], Phase: phaseRunning, Instance: "i1", BatchID: "actual", Elapsed: 20 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native, ExchangeElapsedNanos: &elapsed},
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
