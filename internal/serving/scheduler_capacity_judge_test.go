package serving

import (
	"math"
	"testing"
	"time"
)

func TestCapacityHeuristicFrozenArithmetic(t *testing.T) {
	w := capacityWorkload{d: &Definition{UID: "w", SLO: SLO{Percentile: 100, TargetLatency: 100 * time.Millisecond}}, totalNanos: 20e6, queued: 2, rate: 10}
	for _, tc := range []struct {
		name              string
		mu, service, want float64
	}{
		{"h60ms", 50, 50 * 20e6, 0},
		{"h100ms", 25, 25 * 40e6, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, eligible := capacityHeuristic(w, nil, tc.mu, 10, tc.service, 10*20e6, 0, 1)
			if !eligible || math.Abs(got-tc.want) > 1e-14 {
				t.Fatalf("excess=%g eligible=%t want=%g", got, eligible, tc.want)
			}
		})
	}
}

func TestCapacityHeuristicRetainedDrainAndHorizon(t *testing.T) {
	w := capacityWorkload{d: &Definition{UID: "w", SLO: SLO{Percentile: 100, TargetLatency: time.Second}}, totalNanos: 20e6, queued: 4, rate: 1}
	for _, tc := range []struct {
		name                                       string
		mu, retained, route, period, service, want float64
	}{
		// Retained workers may drain the captured queue during preparation.
		{"retained-drain", 2, 8, 2, 3, 40e6, 0},
		{"retained-deficit", 2, 0, .5, 1, 40e6, 1.54},
		{"overload-horizon", .5, 1, 0, 2, 10e6, 7.04},
		{"route-beyond-horizon", 2, 8, 2, 1, 40e6, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, eligible := capacityHeuristic(w, []float64{0}, tc.mu, tc.retained, tc.service, tc.retained*20e6, tc.route, tc.period)
			if !eligible || math.Abs(got-tc.want) > 1e-13 {
				t.Fatalf("got=%g eligible=%t want=%g", got, eligible, tc.want)
			}
		})
	}
}

func TestCapacityHeuristicZeroSupplyEligibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate     float64
		queued   int
		eligible bool
	}{
		{"empty", 0, 0, true}, {"arrivals", 1, 0, false}, {"captured-backlog", 0, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := capacityWorkload{d: &Definition{SLO: SLO{TargetLatency: time.Second}}, rate: tc.rate, queued: tc.queued}
			var arrivals []float64
			if tc.rate > 0 {
				arrivals = []float64{0}
			}
			excess, eligible := capacityHeuristic(w, arrivals, 0, 0, 0, 0, 0, 1)
			if eligible != tc.eligible || eligible && excess != 0 || !eligible && !math.IsInf(excess, 1) {
				t.Fatalf("excess=%g eligible=%t", excess, eligible)
			}
		})
	}
}

func TestCapacityHeuristicUsesFrozenServiceAndTotalOnce(t *testing.T) {
	w := capacityWorkload{d: &Definition{SLO: SLO{TargetLatency: 100 * time.Millisecond}}, rate: 1, totalNanos: 40e6}
	got, eligible := capacityHeuristic(w, []float64{0}, 10, 10, 10*70e6, 10*70e6, 0, 1)
	if !eligible || math.Abs(got-.1) > 1e-14 {
		t.Fatalf("TOTAL+service must be 110ms: excess=%g eligible=%t", got, eligible)
	}
}
