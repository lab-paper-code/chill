package serving

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// capacitySegmentsForTest replaces the Workload's arrivals with one constant
// stream per ten-second segment of the observation window, oldest first.
func capacitySegmentsForTest(snap *SchedulerSnapshot, window time.Duration, rates ...float64) {
	d := snap.Workloads[0]
	snap.Arrivals = nil
	start := snap.ObservationEnd.Add(-window)
	for k, rate := range rates {
		n := int(rate * 10)
		for j := range n {
			at := start.Add(time.Duration(k)*10*time.Second + time.Duration(j)*10*time.Second/time.Duration(n))
			snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprint(k, "-", j), At: at, Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
		}
	}
}

// Once a step in the arrival rate is detected the estimate is the rate after
// it, and that one value is the candidate demand, the replay's arrivals, the
// energy's request rate and the payback's load: each equals what a steady
// stream at the new rate gives.
func TestDetectedStepIsTheOneLoadEveryConsumerUses(t *testing.T) {
	for _, c := range []struct {
		name  string
		rates []float64
		after float64 // rate after the step
		since time.Duration
	}{
		{"up at the last interval", []float64{5, 5, 5, 5, 5, 30}, 30, 10 * time.Second},
		{"up mid window", []float64{5, 5, 5, 30, 30, 30}, 30, 30 * time.Second},
		{"down at the last interval", []float64{30, 30, 30, 30, 30, 5}, 5, 10 * time.Second},
		{"down mid window", []float64{30, 30, 30, 5, 5, 5}, 5, 30 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
			snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
			snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
			capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 100})

			capacitySegmentsForTest(&snap, s.ObservationWindow, c.rates...)
			d := capacityDecideForTest(t, s, snap)
			p := capacityPlannerForTest(t, s, snap)
			capacitySegmentsForTest(&snap, s.ObservationWindow, c.after, c.after, c.after, c.after, c.after, c.after)
			steady := capacityPlannerForTest(t, s, snap)
			want := c.after

			load := d.Search.Loads[0]
			if !load.WindowStart.Equal(snap.ObservationEnd.Add(-c.since)) || load.InputsPerSecond != want {
				t.Fatalf("estimate: window from %v at %v req/s, want from -%v at %v", load.WindowStart, load.InputsPerSecond, c.since, want)
			}
			if got := d.Capacity.Workloads[0].Rate; got != want { // candidate demand
				t.Fatalf("decision demand %v, want %v", got, want)
			}
			if got := p.searchProblem(time.Time{}).Workloads[0].Rate; got != want {
				t.Fatalf("search demand %v, want %v", got, want)
			}
			if got := d.Target.Feedback[0].ObservedArrivals; got != load.ComputeInputs || float64(got) != want*c.since.Seconds() { // replay arrivals
				t.Fatalf("replay arrivals %d, want the %d of the new rate", got, int(want*c.since.Seconds()))
			}
			keep := make([]int, len(p.plans))
			_, final := p.requestRates(p.evaluate(keep, 0))
			sum := 0.0
			for _, r := range final {
				sum += r.rate
			}
			if sum != want { // energy
				t.Fatalf("energy request rate %v, want %v", sum, want)
			}
			if got, ref := p.loadJoules(keep, 0), steady.loadJoules(keep, 0); got != ref { // payback
				t.Fatalf("payback load joules %v, steady stream %v", got, ref)
			}
		})
	}
}

// poissonTimes is a Poisson stream of the given rate over [start, start+window).
func poissonTimes(rng *rand.Rand, start time.Time, window time.Duration, rate float64) []time.Time {
	var times []time.Time
	for at := rng.ExpFloat64() / rate; at < window.Seconds(); at += rng.ExpFloat64() / rate {
		times = append(times, start.Add(time.Duration(at*float64(time.Second))))
	}
	return times
}

// At a constant Poisson rate a window is called changed at most with the
// significance the SLO percentile leaves, (1 - percentile/100): each of the
// k boundaries is an exact test at that level over k, so by the union bound a
// window of k boundaries rejects with probability at most the level. The
// count alarms of N seeded windows is then below the level times N plus four
// binomial standard deviations (false alarm probability under 1e-4 for a true
// rate at the level).
func TestChangeDetectionFalseAlarmRateStaysUnderItsSignificance(t *testing.T) {
	const windows = 2000
	end := time.Unix(100000, 0)
	for _, rate := range []float64{30, 0.3} {
		t.Run(fmt.Sprint(rate, " req/s"), func(t *testing.T) {
			s := SchedulerSettings{ObservationWindow: time.Minute, ControlInterval: 10 * time.Second}
			d := &Definition{UID: "w", SLO: SLO{Percentile: 95}}
			level := 1 - d.SLO.Percentile/100
			rng := rand.New(rand.NewSource(1))
			alarms := 0
			for range windows {
				start := end.Add(-s.ObservationWindow)
				since := schedulerLoadStart(s.ControlInterval, d, poissonTimes(rng, start, s.ObservationWindow, rate), start, end, time.Time{})
				if !since.Equal(start) {
					alarms++
				}
			}
			bound := level*windows + 4*math.Sqrt(windows*level*(1-level))
			if float64(alarms) > bound {
				t.Fatalf("%d false alarms in %d windows at %v req/s, above %v for level %v", alarms, windows, rate, bound, level)
			}
			t.Logf("%d of %d windows", alarms, windows)
		})
	}
}

// A real step is found: at the significance the SLO percentile leaves, a step
// of a third (30 to 39 req/s) starts the load at the first decision after it,
// and a tripling of a sparse stream (0.5 to 1.5 req/s) within three, in at
// least 90 % of 400 seeded Poisson streams. A level stricter than (1 -
// percentile/100) over the boundaries misses more (one over the cube of the
// boundaries: 79 % and 67 %).
func TestChangeDetectionFindsARealStep(t *testing.T) {
	const streams = 400
	for _, c := range []struct {
		before, after float64
		intervals     int
	}{{30, 39, 1}, {0.5, 1.5, 3}} {
		t.Run(fmt.Sprint(c.before, " to ", c.after, " req/s"), func(t *testing.T) {
			s := SchedulerSettings{ObservationWindow: time.Minute, ControlInterval: 10 * time.Second}
			d := &Definition{UID: "w", SLO: SLO{Percentile: 95}}
			step := time.Unix(100000, 0)
			rng := rand.New(rand.NewSource(1))
			found := 0
			for range streams {
				times := append(poissonTimes(rng, step.Add(-s.ObservationWindow), s.ObservationWindow, c.before),
					poissonTimes(rng, step, time.Duration(c.intervals)*s.ControlInterval, c.after)...)
				for k := 1; k <= c.intervals; k++ {
					end := step.Add(time.Duration(k) * s.ControlInterval)
					start := end.Add(-s.ObservationWindow)
					var window []time.Time
					for _, at := range times {
						if !at.Before(start) && at.Before(end) {
							window = append(window, at)
						}
					}
					if !schedulerLoadStart(s.ControlInterval, d, window, start, end, time.Time{}).Before(step) {
						found++
						break
					}
				}
			}
			if found < streams*9/10 {
				t.Fatalf("step found within %d intervals in %d of %d streams, below 90 %%", c.intervals, found, streams)
			}
			t.Logf("%d of %d streams", found, streams)
		})
	}
}

// evenTimesForTest places rate arrivals per second evenly over [from, to).
func evenTimesForTest(from, to time.Time, rate float64) []time.Time {
	n := int(math.Round(rate * to.Sub(from).Seconds()))
	times := make([]time.Time, n)
	for j := range times {
		times[j] = from.Add(time.Duration(j) * to.Sub(from) / time.Duration(n))
	}
	return times
}

// loadsOverDecisionsForTest decides at every control interval of ends over the
// arrivals (times) of snap's Workload and returns each decision's load. The
// latest detected change still inside the observation window is carried to the
// next decision, as the controller carries it (kube rememberLoadChanges,
// annotateHistory).
func loadsOverDecisionsForTest(s SchedulerSettings, snap SchedulerSnapshot, times []time.Time, ends []time.Time) []SchedulerLoad {
	d := snap.Workloads[0]
	var changed time.Time
	loads := make([]SchedulerLoad, 0, len(ends))
	for _, end := range ends {
		snap.At, snap.ObservationEnd, snap.Arrivals = end, end, nil
		for j, at := range times {
			if !at.Before(end.Add(-s.ObservationWindow)) && at.Before(end) {
				snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprint(j), At: at, Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
			}
		}
		if !changed.After(end.Add(-s.ObservationWindow)) {
			changed = time.Time{}
		}
		snap.LoadChangedAt = map[string]time.Time{d.UID: changed}
		search := schedulerSearchPopulation(context.Background(), s, snap)
		load := search.Loads[0]
		if load.WindowStart.After(SchedulerLoadStretchStart(search.WindowStart, search.WindowEnd, changed)) {
			changed = load.WindowStart
		}
		loads = append(loads, load)
	}
	return loads
}

// The load window is the stretch since the last detected change, and the next
// change is looked for inside it, so a second change inside the observation
// window starts the load as soon as a decision sees it. In a fall and rise 30 s
// apart, a 15 s pause and a 10 s burst that ends, every decision whose last
// control interval holds one rate estimates that rate within 10 %.
func TestEachChangeStartsTheLoadAtTheNextDecision(t *testing.T) {
	type segment struct {
		until, rate float64 // seconds; rate holds from the previous segment's end
	}
	for _, c := range []struct {
		name     string
		segments []segment
	}{
		{"fall then rise within 30 s", []segment{{200, 30}, {230, 5}, {400, 30}}},
		{"15 s pause", []segment{{203, 30}, {218, 0}, {400, 30}}},
		{"10 s burst that ends", []segment{{200, 5}, {210, 30}, {400, 5}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ObservationWindow, s.ControlInterval = time.Minute, 10*time.Second
			base := time.Unix(100000, 0)
			at := func(seconds float64) time.Time { return base.Add(time.Duration(seconds * float64(time.Second))) }
			var times []time.Time
			from := 0.0
			for _, g := range c.segments {
				times = append(times, evenTimesForTest(at(from), at(g.until), g.rate)...)
				from = g.until
			}
			var ends []time.Time
			for second := 210.0; second <= 300; second += 10 {
				ends = append(ends, at(second))
			}
			for k, load := range loadsOverDecisionsForTest(s, snap, times, ends) {
				end := ends[k].Sub(base).Seconds()
				truth, from := -1.0, 0.0 // the one rate of the last control interval
				for _, g := range c.segments {
					if from <= end-s.ControlInterval.Seconds() && end <= g.until {
						truth = g.rate
					}
					from = g.until
				}
				if truth < 0 {
					continue // a change inside the last interval
				}
				if math.Abs(load.InputsPerSecond-truth) > .1*truth {
					t.Fatalf("decision at %v s: %v req/s from %v s, the rate is %v", end, load.InputsPerSecond, load.WindowStart.Sub(base).Seconds(), truth)
				}
			}
		})
	}
}

// After a detected step the arrivals before it never enter the next test, so a
// steady stream after it finds no further change: the load window stays at the
// step while the step is inside the observation window, then follows the
// window's start back to the full observation window.
func TestLoadWindowGrowsBackAfterAChange(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Minute, 10*time.Second
	base := time.Unix(100000, 0)
	at := func(seconds float64) time.Time { return base.Add(time.Duration(seconds * float64(time.Second))) }
	times := append(evenTimesForTest(at(0), at(200), 5), evenTimesForTest(at(200), at(400), 30)...)
	var ends []time.Time
	for second := 210.0; second <= 330; second += 10 {
		ends = append(ends, at(second))
	}
	loads := loadsOverDecisionsForTest(s, snap, times, ends)
	step := loads[0].WindowStart
	if step.Before(at(190)) || step.After(at(210)) {
		t.Fatalf("fixture: the first decision did not detect the step near 200 s: %v s", step.Sub(base).Seconds())
	}
	for k, load := range loads[1:] {
		want := step
		if start := ends[k+1].Add(-s.ObservationWindow); start.After(step) {
			want = start
		}
		if !load.WindowStart.Equal(want) {
			t.Fatalf("decision at %v s: the load window starts at %v s, want %v s", ends[k+1].Sub(base).Seconds(), load.WindowStart.Sub(base).Seconds(), want.Sub(base).Seconds())
		}
	}
}

// The measured verdict covers the load window: after a step from 20 to 80
// req/s, the late completions before the step say nothing of the serving at
// the new load. The 80 on-time completions of the stretch after it, up to one
// SLO target before the end, judge it.
func TestMeasuredVerdictCoversTheLoadWindow(t *testing.T) {
	s, snap := measuredFixture()
	s.ControlInterval = time.Second
	d := snap.Workloads[0]
	start := snap.ObservationEnd.Add(-s.ObservationWindow)
	step := start.Add(2 * time.Second)
	snap.Arrivals = nil
	for j, at := range append(evenTimesForTest(start, step, 20), evenTimesForTest(step, snap.ObservationEnd, 80)...) {
		elapsed := 10 * time.Millisecond
		if at.Before(step) {
			elapsed = 2 * d.SLO.TargetLatency
		}
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprint(j), At: at, Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID, ClientOutcome: outcomeCompleted, ClientElapsed: elapsed})
	}
	if load := schedulerSearchPopulation(context.Background(), s, snap).Loads[0]; !load.WindowStart.Equal(step) {
		t.Fatalf("fixture: load window from %v, not the step at %v", load.WindowStart, step)
	}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || !m.Since.Equal(step) || m.Population != 80 || m.OnTime != 80 || !m.Satisfied || m.OnTimeRate != 80 {
		t.Fatalf("the verdict must be the 80 on-time completions after the step: %+v", m)
	}
}

// Settings without a positive control interval and SLO percentiles outside
// (0, 100] are rejected where a decision or a replay starts, before any load
// window is looked for.
func TestInvalidSettingsAndSLOsStopBeforeTheLoadWindow(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow = time.Minute
	capacitySegmentsForTest(&snap, s.ObservationWindow, 5, 5, 5, 5, 5, 5)
	for _, interval := range []time.Duration{0, -time.Second} {
		bad := s
		bad.ControlInterval = interval
		if d, err := EvaluateScheduler(context.Background(), bad, snap); err != nil || len(d.Reasons) != 1 || d.Search.Loads != nil {
			t.Fatalf("control interval %v: %v %+v", interval, err, d.Reasons)
		}
		keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances, Routing: snap.Routing}
		if r, err := EvaluateFeedbackCandidate(context.Background(), bad, snap, keep, 0); err != nil || len(r.InvalidReasons) != 1 {
			t.Fatalf("control interval %v replayed: %v %+v", interval, err, r)
		}
	}
	for _, percentile := range []float64{-1, 0, 100.5, math.NaN()} {
		bad := *snap.Workloads[0]
		bad.SLO.Percentile = percentile
		snap := snap
		snap.Workloads = []*Definition{&bad}
		snap.Arrivals = slices.Clone(snap.Arrivals)
		for k := range snap.Arrivals {
			snap.Arrivals[k].Definition = &bad
		}
		if d, err := EvaluateScheduler(context.Background(), s, snap); err != nil || !slices.Contains(d.Reasons, "InvalidSLO:"+bad.UID) {
			t.Fatalf("percentile %v: %v %+v", percentile, err, d.Reasons)
		}
	}
}
