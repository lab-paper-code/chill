package serving

import (
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// The actor declares running before pack -> IPC. Exercise the real runtime
// snapshot adapter, not a replacement running-residual projection.
// Every ready actor has a last service end (warm-up ends at ready), so both
// packing and the started exchange are queried at the actor's 3ms idle age.
// The profile holds one 95th-percentile window (59 records) per law: 100ms
// after a 3ms gap and 2ms after no gap. Nearest-window selection at 3ms takes
// the 100ms law; querying any other gap would give 2ms + 2ms elapsed = 4ms.
func TestPendingExchangePreservesKnownActorGapClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started bool
	}{
		{"packing known actor", false},
		{"just started known actor", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.AutonomousWorkloads = nil
			settings.ComparisonPeriod = 200 * time.Millisecond
			settings.CompletionMarginMS, settings.DefaultDispatchOccupancyNanos = 0, 0
			d := snapshot.Workloads[0]
			d.SLO = SLO{Percentile: 95, TargetLatency: 50 * time.Millisecond}
			// Previous exchange ended -3ms, gateway request entered -2ms,
			// actor declared running -1ms; at snapshot 0ms packing is pending.
			lastEndAge, zeroElapsed, busy := int64(3*time.Millisecond), int64(0), int64(0)
			samples := make([]firstfloor.ServiceSample, 0, 118)
			for range 59 {
				samples = append(samples,
					firstfloor.ServiceSample{ServiceDurationNanos: int64(2 * time.Millisecond), PrecedingIdleNanos: &busy, TrialID: "physical-a"},
					firstfloor.ServiceSample{ServiceDurationNanos: int64(100 * time.Millisecond), PrecedingIdleNanos: &lastEndAge, TrialID: "physical-a"})
			}
			b := &snapshot.Instances[0].Profile.Batches[0]
			*b = schedulerTestPairedBatch(*b, samples)
			snapshot.Instances[0].LastServiceEndAgeNanos = &lastEndAge
			native := &firstfloor.State{LastServiceEndAgeNanos: &lastEndAge,
				Requests: []firstfloor.RequestState{{
					Context: firstfloor.RequestContext{
						RequestID: "running", WorkloadUID: d.UID, Definition: d.Identity(), SenderElapsedMS: 1,
					},
					Phase: phaseRunning, BatchID: 2, BatchSize: 1, ElapsedMS: 1, ServiceElapsedMS: 1,
				}}}
			if tc.started {
				native.Requests[0].PrecedingIdleNanos = &lastEndAge
				native.Requests[0].ExchangeElapsedNanos = &zeroElapsed
			}
			call := &Call{
				receipt:    Receipt{ID: "running", UID: d.UID, ReceivedAt: snapshot.At.Add(-2 * time.Millisecond)},
				definition: d, identity: d.Identity(), assignment: &Assignment{Instance: "i1"},
			}
			runtime := &Runtime{
				calls: map[string]*Call{"running": call},
				instances: map[string]*runtimeInstance{"i1": {ReadyInstance: ReadyInstance{State: InstanceState{
					ID: "i1", ObservedAt: snapshot.At, Native: native,
				}}}},
			}
			snapshot.Outstanding = runtime.SchedulerInputs(func() time.Time { return snapshot.At }).Outstanding
			got := schedulerKeepForTest(t, settings, snapshot)
			estimate := schedulerRequireEstimate(t, got, d.UID)
			if len(got.Reasons) != 0 || got.Status != SchedulerStatusOverload || estimate.ExpectedNanos == nil ||
				*estimate.ExpectedNanos != float64(102*time.Millisecond) {
				t.Fatalf("packing and just-started service must retain the same actor-history law: status=%s estimate=%+v reasons=%v",
					got.Status, estimate, got.Reasons)
			}
			if !tc.started && !strings.Contains(strings.Join(got.Assumptions, "\n"),
				"pending native exchange uses current idle-age lower-bound approximation") {
				t.Fatalf("pending G must be labelled approximate, not an observed exchange gap: %v", got.Assumptions)
			}
		})
	}
}
