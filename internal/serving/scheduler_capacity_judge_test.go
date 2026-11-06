package serving

import (
	"math"
	"math/rand/v2"
	"testing"
)

type capacityJudgeSample struct {
	w             capacityWorkload
	knew, kret, r float64
	present       bool
}

func capacityJudgeSamples() []capacityJudgeSample {
	const randomCount = 10000
	samples := make([]capacityJudgeSample, 0, randomCount+2592)
	rng := rand.New(rand.NewPCG(1, 20261107))
	positive := func() float64 {
		return math.Ldexp(0.5+rng.Float64(), rng.IntN(21)-10)
	}
	for range randomCount {
		samples = append(samples, capacityJudgeSample{
			w: capacityWorkload{
				rate: positive(), queued: rng.IntN(1000), slack: rng.Float64()*32 - 8,
			},
			knew: positive(), kret: positive(), r: positive(), present: rng.IntN(2) == 0,
		})
	}
	// The Cartesian product includes zero capacity, empty backlog, nonpositive
	// slack, zero arrival rate, absent routes, and immediate routing together.
	for _, rate := range []float64{0, 0.5, 2} {
		for _, queued := range []int{0, 1, 4} {
			for _, slack := range []float64{-1, 0, 0.25, 2} {
				for _, kret := range []float64{0, 0.5, 2} {
					for _, r := range []float64{0, 0.5, 2} {
						for _, knew := range []float64{0, 0.5, 2, 8} {
							for _, present := range []bool{false, true} {
								samples = append(samples, capacityJudgeSample{
									w:    capacityWorkload{rate: rate, queued: queued, slack: slack},
									knew: knew, kret: kret, r: r, present: present,
								})
							}
						}
					}
				}
			}
		}
	}
	return samples
}

func (s capacityJudgeSample) backlog() float64 {
	return float64(s.w.queued) + max(s.w.rate-s.kret, 0)*s.r
}

func (s capacityJudgeSample) bound() float64 {
	if s.backlog() == 0 {
		return s.w.rate
	}
	if s.w.slack <= 0 {
		return math.Inf(1)
	}
	return max(s.w.rate, s.backlog()/s.w.slack)
}

func (s capacityJudgeSample) closedSatisfied() bool {
	backlog := s.backlog()
	if !s.present || (backlog > 0 && s.w.slack <= 0) {
		return false
	}
	if s.knew == 0 {
		return s.w.rate == 0 && backlog == 0
	}
	// Evaluate kappa >= max(lambda, B/slack) in v1's division order.
	// No near-boundary samples are excluded, including adjacent float64s.
	return s.w.rate/s.knew <= 1 && (backlog == 0 || backlog/s.knew/s.w.slack <= 1)
}

func TestCapacityJudgeSatisfiedEquivalence(t *testing.T) {
	samples := capacityJudgeSamples()
	probes := 0
	check := func(s capacityJudgeSample) {
		t.Helper()
		got := capacityJudge(s.w, s.knew, s.kret, s.present, s.r).satisfied
		if want := s.closedSatisfied(); got != want {
			t.Fatalf("sample=%+v B=%.17g bound=%.17g satisfied=%t closedSatisfied=%t", s, s.backlog(), s.bound(), got, want)
		}
	}
	for _, s := range samples {
		check(s)
		bound := s.bound()
		if bound <= 0 || math.IsInf(bound, 1) {
			continue
		}
		for _, knew := range []float64{math.Nextafter(bound, 0), bound, math.Nextafter(bound, math.Inf(1))} {
			s.knew = knew
			check(s)
			probes++
		}
	}
	t.Logf("random=10000 Cartesian boundaries=%d adjacent-boundary probes=%d total=%d", len(samples)-10000, probes, len(samples)+probes)
}

func (s capacityJudgeSample) closedWorst() (string, float64) {
	backlog := s.backlog()
	switch {
	case !s.present:
		return "absent", math.Inf(1)
	case s.knew == 0 && s.w.rate == 0 && backlog == 0:
		return "zero capacity empty", 0
	case s.knew == 0:
		return "zero capacity nonempty", math.Inf(1)
	case backlog == 0:
		return "zero backlog", max(s.w.rate/s.knew-1, 0)
	case s.w.slack <= 0:
		return "nonpositive slack", math.Inf(1)
	default:
		return "positive backlog and slack", s.bound()/s.knew - 1
	}
}

func TestCapacityJudgeWorstValue(t *testing.T) {
	counts := map[string]int{
		"absent": 0, "zero capacity empty": 0, "zero capacity nonempty": 0,
		"zero backlog": 0, "nonpositive slack": 0, "positive backlog and slack": 0,
	}
	samples := capacityJudgeSamples()
	for _, s := range samples {
		category, want := s.closedWorst()
		counts[category]++
		got := capacityJudge(s.w, s.knew, s.kret, s.present, s.r).worst()
		if got == want {
			continue
		}
		if math.IsNaN(got) || math.IsInf(got, 0) || math.IsInf(want, 0) || math.Abs(got-want) > 1e-12*math.Abs(want) {
			t.Fatalf("category=%q sample=%+v B=%.17g bound=%.17g worst=%.17g closedWorst=%.17g", category, s, s.backlog(), s.bound(), got, want)
		}
	}
	for category, count := range counts {
		if count == 0 {
			t.Errorf("category %q was not checked", category)
		}
		t.Logf("category=%q samples=%d", category, count)
	}
	t.Logf("random=10000 Cartesian boundaries=%d total=%d", len(samples)-10000, len(samples))
}
