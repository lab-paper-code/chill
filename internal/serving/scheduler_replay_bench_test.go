package serving

import (
	"context"
	"fmt"
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
			for range b.N {
				if _, err := EvaluateScheduler(context.Background(), s, snap); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func schedulerReplayWindow(n int) (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads, s.Replays, s.MaxReplayRequests = nil, 1, n+1
	gap := time.Millisecond
	window := time.Duration(n) * gap
	s.ObservationWindow, s.Horizon, s.MaxObservationAge = window, window, time.Hour
	p := *snap.Instances[0].Profile
	p.Batches = []BatchProfile{{Batch: 1, DurationNanos: []float64{6e5, 8e5, 1e6}, EnergyPerInputJoules: 0.01, MemoryBytes: 100}}
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	d := snap.Workloads[0]
	start := snap.ObservationEnd.Add(-window)
	for j := range n {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprintf("a%07d", j), At: start.Add(time.Duration(j) * gap), Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID, Lifetime: time.Second})
	}
	return s, snap
}
