package serving

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

// BenchmarkSchedulerReplayWindow replays one keep window of n arrivals at
// about 80% of the instance's service rate; run with -bench to see that the
// time per request stays flat from 2,000 to 8,000 requests.
func BenchmarkSchedulerReplayWindow(b *testing.B) {
	for _, n := range []int{2000, 8000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s, snap := schedulerReplayWindow(n)
			keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}
			for range b.N {
				if _, err := schedulerReplayEstimate(context.Background(), s, snap, keep, 1, nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func schedulerReplayWindow(n int) (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	gap := time.Millisecond
	window := time.Duration(n) * gap
	s.ObservationWindow, s.ComparisonPeriod = window, window
	p := *snap.Instances[0].Profile
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 0.01, MemoryBytes: 100}, []float64{6e5, 8e5, 1e6})}
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	d := snap.Workloads[0]
	start := snap.ObservationEnd.Add(-window)
	for j := range n {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprintf("a%07d", j), At: start.Add(time.Duration(j) * gap), Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID, Lifetime: time.Second})
	}
	return s, snap
}

func blockedReplayDispatch(tb testing.TB, executors, queued int, full bool) *schedulerReplayState {
	tb.Helper()
	s := replayAdmissionState(tb)
	prototype := *s.instances[0]
	s.instances = nil
	s.byID = make(map[string]*schedulerReplayInstance)
	definition := s.snap.Workloads[0]
	definition.Admission.MaxQueuedRequests = int32(queued)
	for j := range executors {
		instance := prototype
		instance.config.ID = fmt.Sprintf("executor-%d", j)
		if full {
			instance.outstanding = instance.config.Capacity
		} else {
			instance.outstanding = 1
			instance.exchangeKnown, instance.batchElapsed = true, int64(time.Second)
			instance.batch = []*schedulerReplayRequest{{source: SchedulerRequest{ID: fmt.Sprintf("running-%d", j), Definition: definition}, identity: definition.Identity(), running: true}}
		}
		s.instances = append(s.instances, &instance)
		s.byID[instance.config.ID] = &instance
	}
	for j := range queued {
		request := replayAdmissionPending(s, fmt.Sprintf("queued-%d", j), definition)
		request.expires = math.Inf(1)
	}
	return s
}

func TestReplayDispatchCachesUnknownExecutorsBeforeScanningQueue(t *testing.T) {
	for _, full := range []bool{false, true} {
		s := blockedReplayDispatch(t, 64, 4096, full)
		ctx := &capacityCountingContext{Context: context.Background()}
		if !s.routeRequests(ctx) || ctx.calls > 2*len(s.instances)+8 {
			t.Fatalf("blocked dispatch repeatedly scanned the large queue: calls=%d executors=%d", ctx.calls, len(s.instances))
		}
		for _, request := range s.live {
			if request.dispatched || request.dropped {
				t.Fatal("unknown/no-capacity executor fabricated admission or rejection")
			}
		}
	}
}

func BenchmarkReplayDispatchBlockedExecutors(b *testing.B) {
	for _, executors := range []int{32, 128} {
		for _, queued := range []int{1024, 4096} {
			for _, full := range []bool{false, true} {
				b.Run(fmt.Sprintf("executors=%d/queued=%d/full=%t", executors, queued, full), func(b *testing.B) {
					s := blockedReplayDispatch(b, executors, queued, full)
					ctx := context.Background()
					s.routeRequests(ctx) // allocate reusable executor indexes before measuring
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if !s.routeRequests(ctx) {
							b.Fatal("dispatch canceled")
						}
					}
				})
			}
		}
	}
}

func TestReplayAdmissionProjectionDoesNotAllocatePerCandidate(t *testing.T) {
	s := replayAdmissionState(t)
	r := replayAdmissionPending(s, "candidate", s.snap.Workloads[0])
	ctx := context.Background()
	var estimate AdmissionEstimate
	allocations := testing.AllocsPerRun(100, func() {
		s.beginAdmission(ctx)
		estimate = s.estimateAdmission(s.instances[0], r)
		s.endAdmission()
	})
	if !estimate.Known || allocations != 0 {
		t.Fatalf("warm dispatch projection: estimate=%+v allocations=%g, want 0", estimate, allocations)
	}
}
