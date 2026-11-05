package serving

import (
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// The actor declares running before pack -> IPC. Exercise the real runtime
// snapshot adapter, not a replacement running-residual projection.
func TestPendingExchangePreservesKnownActorGapClassification(t *testing.T) {
	for _, tc := range []struct {
		name             string
		history, started bool
		want             time.Duration
	}{
		{"packing known actor", true, false, 102 * time.Millisecond},
		{"just started known actor", true, true, 102 * time.Millisecond},
		{"packing new actor", false, false, 4 * time.Millisecond},
		{"just started new actor", false, true, 4 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.AutonomousWorkloads = nil
			settings.ComparisonPeriod = 200 * time.Millisecond
			settings.CompletionMarginMS, settings.DefaultDispatchOccupancyNanos = 0, 0
			d := snapshot.Workloads[0]
			d.SLO = SLO{Percentile: 100, TargetLatency: 50 * time.Millisecond}
			busy := int64(0)
			b := &snapshot.Instances[0].Profile.Batches[0]
			*b = schedulerTestPairedBatch(*b, []firstfloor.ServiceSample{
				{ServiceDurationNanos: int64(2 * time.Millisecond), TrialID: "physical-a"},
				{ServiceDurationNanos: int64(100 * time.Millisecond), PrecedingIdleNanos: &busy, TrialID: "physical-a"},
			})
			// Previous exchange ended -3ms, gateway request entered -2ms,
			// actor declared running -1ms; at snapshot 0ms packing is pending.
			knownAge, zeroElapsed := int64(3*time.Millisecond), int64(0)
			var lastEndAge *int64
			if tc.history {
				lastEndAge = &knownAge
			}
			snapshot.Instances[0].LastServiceEndAgeNanos = lastEndAge
			native := &firstfloor.State{LastServiceEndAgeNanos: lastEndAge,
				Requests: []firstfloor.RequestState{{
					Context: firstfloor.RequestContext{
						RequestID: "running", WorkloadUID: d.UID, Definition: d.Identity(), SenderElapsedMS: 1,
					},
					Phase: phaseRunning, BatchID: 2, BatchSize: 1, ElapsedMS: 1, ServiceElapsedMS: 1,
				}}}
			if tc.started {
				native.Requests[0].PrecedingIdleNanos = lastEndAge
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
			wantStatus := schedulerStatusFeasible
			if tc.history {
				wantStatus = SchedulerStatusOverload
			}
			if len(got.Reasons) != 0 || got.Status != wantStatus || estimate.ExpectedNanos == nil ||
				*estimate.ExpectedNanos != float64(tc.want) {
				t.Fatalf("packing and just-started service must retain the same actor-history law: status=%s estimate=%+v reasons=%v",
					got.Status, estimate, got.Reasons)
			}
			if got.ConditionalService == nil || (got.ConditionalService.StartupQueries == 0) != tc.history {
				t.Fatalf("only an actor without a previous exchange anchor is startup: %+v", got.ConditionalService)
			}
			if tc.history && !tc.started && !strings.Contains(strings.Join(got.Assumptions, "\n"),
				"pending native exchange uses current idle-age lower-bound approximation") {
				t.Fatalf("pending G must be labelled approximate, not an observed exchange gap: %v", got.Assumptions)
			}
		})
	}
}
