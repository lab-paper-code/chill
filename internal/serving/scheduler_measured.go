package serving

import (
	"cmp"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// SchedulerServingShare is one instance serving a Workload: its profile, its
// batch cap and its route weight. The profile ID fixes the Node, device, power
// mode and measured condition. An instance ID is not part of it: a re-created
// instance of the same profile and cap serves the same way. The batch wait is
// not part of it. A remembered miss is about the set, so its shares carry no
// weight.
type SchedulerServingShare struct {
	ProfileID string `json:"profileID"`
	MaxBatch  int    `json:"maxBatch"`
	Weight    int32  `json:"weight,omitempty"`
}

// SchedulerMeasurement is a measured fact about a Definition served as
// Serving.
//
// The window's measurement holds its client-reported outcomes over its
// compute arrivals from Since to the end of the observation window: OnTime and
// Late are completions within and beyond the SLO target, every other report
// and every unreported arrival older than the target is a failure. OnTimeRate
// is its on-time completions per second and Satisfied the empirical rank rule
// over Population. It judges every result that serves the Workload as it runs
// at a demand it speaks for (speaksFor).
//
// A remembered miss holds only the set and the most it delivered on time,
// OnTimeRate (remember). It judges every result whose instances for the
// Workload it covers while the result's demand needs more on time than that.
// This assumes that lowering a batch cap, removing an instance or shifting
// shares among the same instances does not raise what they deliver on time.
// Where that is wrong, the planner adds capacity instead of re-batching or
// shrinking the set: it spends energy, never the SLO. The replay judges where
// no fact applies.
type SchedulerMeasurement struct {
	DefinitionID string                  `json:"definitionID"`
	Serving      []SchedulerServingShare `json:"serving"`
	OnTimeRate   float64                 `json:"onTimeRate"`
	Since        *time.Time              `json:"since,omitempty"`
	Population   int                     `json:"population,omitempty"`
	OnTime       int                     `json:"onTime,omitempty"`
	Late         int                     `json:"late,omitempty"`
	Satisfied    bool                    `json:"satisfied"`
}

// covers reports whether the options are instances of m's set at no larger
// batch caps: each option takes its own instance of the same profile with an
// equal or larger cap.
func (m SchedulerMeasurement) covers(options []SchedulerServingShare) bool {
	if len(options) == 0 {
		return false
	}
	left := slices.Clone(m.Serving)
	// Options are sorted, so within a profile the largest cap comes first; an
	// instance that takes it takes every smaller cap of that profile too.
	for k := len(options) - 1; k >= 0; k-- {
		at := slices.IndexFunc(left, func(s SchedulerServingShare) bool {
			return s.ProfileID == options[k].ProfileID && options[k].MaxBatch <= s.MaxBatch
		})
		if at < 0 {
			return false
		}
		left = slices.Delete(left, at, at+1)
	}
	return true
}

// speaksFor reports whether the window's measurement, taken at the observed
// demand, judges the Workload at the planned demand rate: a met window only
// for a demand it saw, a miss for any demand at least as high.
func (m SchedulerMeasurement) speaksFor(rate, observed float64) bool {
	if m.Satisfied {
		return rate <= observed
	}
	return rate >= observed
}

// servingShares lists the routed options of routes in a fixed order.
func servingShares(routes map[string]int32, option func(id string) (SchedulerServingShare, bool)) []SchedulerServingShare {
	shares := make([]SchedulerServingShare, 0, len(routes))
	for id, weight := range routes {
		if share, ok := option(id); ok && weight > 0 {
			share.Weight = weight
			shares = append(shares, share)
		}
	}
	slices.SortFunc(shares, func(a, b SchedulerServingShare) int {
		return cmp.Or(cmp.Compare(a.ProfileID, b.ProfileID), cmp.Compare(a.MaxBatch, b.MaxBatch), cmp.Compare(a.Weight, b.Weight))
	})
	return shares
}

// sameServing reports the same options with the same shares of the traffic.
// Weights route by their ratios, so scaling them all is the same serving;
// shifting load between the same options is not.
func sameServing(a, b []SchedulerServingShare) bool {
	if !sameSet(a, b) {
		return false
	}
	totalA, totalB := int64(0), int64(0)
	for k := range a {
		totalA, totalB = totalA+int64(a[k].Weight), totalB+int64(b[k].Weight)
	}
	for k := range a {
		if int64(a[k].Weight)*totalB != int64(b[k].Weight)*totalA {
			return false
		}
	}
	return true
}

// sameSet reports the same options, whatever their weights.
func sameSet(a, b []SchedulerServingShare) bool {
	return slices.EqualFunc(a, b, func(x, y SchedulerServingShare) bool {
		return x.ProfileID == y.ProfileID && x.MaxBatch == y.MaxBatch
	})
}

// windowMeasurement is d's measurement on the running serving, or nil when
// there is no window or it holds fewer reports than identify d's SLO
// percentile (MinimumServiceSamples). The window starts at the last action's
// end. It exists only while every action has ended and every routed instance
// of d is routable for it: otherwise the serving it would measure is not
// settled.
func windowMeasurement(snapshot SchedulerSnapshot, search SchedulerSearch, d *Definition, routes map[string]int32, serving []SchedulerServingShare) *SchedulerMeasurement {
	since := search.WindowStart
	for _, a := range snapshot.Actions {
		if a.EndedAt == nil {
			return nil
		}
		if a.EndedAt.After(since) {
			since = *a.EndedAt
		}
	}
	for _, i := range snapshot.Instances {
		if routes[i.ID] > 0 && !i.Routable[d.UID] {
			return nil
		}
	}
	m, reports := SchedulerMeasurement{DefinitionID: d.Identity(), Serving: serving, Since: &since}, 0
	for _, a := range snapshot.Arrivals {
		if !a.computeInput() || a.Definition.UID != d.UID || a.At.Before(since) || !a.At.Before(search.WindowEnd) {
			continue
		}
		switch {
		case a.ClientOutcome == outcomeCompleted && a.ClientElapsed <= a.Definition.SLO.TargetLatency:
			m.OnTime++
		case a.ClientOutcome == outcomeCompleted:
			m.Late++
		case a.ClientOutcome == "" && (a.Outcome != "" || search.WindowEnd.Sub(a.At) <= a.Definition.SLO.TargetLatency):
			// Unreported and still able to finish on time, or finished at the
			// server with its client's outcome unknown.
			continue
		}
		m.Population++
		if a.ClientOutcome != "" {
			reports++
		}
	}
	if minimum, err := firstfloor.MinimumServiceSamples(d.SLO.Percentile); err != nil || reports < minimum || len(serving) == 0 {
		return nil
	}
	m.OnTimeRate = float64(m.OnTime) / search.WindowEnd.Sub(since).Seconds()
	m.Satisfied = m.OnTime >= empiricalSLORank(m.Population, d.SLO.Percentile)
	return &m
}

// remember folds the window's measurements into the remembered misses. A set
// that missed is remembered once by its Definition, its options and the most
// it delivered on time; any later window of the same set that delivers more on
// time raises OnTimeRate, and one that delivers less changes nothing. A fact
// lasts while its Definition is in the snapshot: an SLO, admission or lifetime
// change is a new Definition generation. A fact naming a profile no longer in
// the catalog covers no re-measured profile, which has a new ID.
func remember(snapshot SchedulerSnapshot, window []SchedulerMeasurement) []SchedulerMeasurement {
	defined := map[string]bool{}
	for _, d := range snapshot.Workloads {
		if d != nil {
			defined[d.Identity()] = true
		}
	}
	facts := slices.DeleteFunc(slices.Clone(snapshot.Measurements), func(m SchedulerMeasurement) bool { return !defined[m.DefinitionID] })
	for _, m := range window {
		at := slices.IndexFunc(facts, func(old SchedulerMeasurement) bool {
			return old.DefinitionID == m.DefinitionID && sameSet(old.Serving, m.Serving)
		})
		switch {
		case at >= 0:
			facts[at].OnTimeRate = max(facts[at].OnTimeRate, m.OnTimeRate)
		case !m.Satisfied:
			set := slices.Clone(m.Serving)
			for k := range set {
				set[k].Weight = 0
			}
			facts = append(facts, SchedulerMeasurement{DefinitionID: m.DefinitionID, Serving: set, OnTimeRate: m.OnTimeRate})
		}
	}
	return facts
}

// judged returns the population, on-time and completed counts and the verdict
// that judge the Workload: the measurement when one applies, else the replay's.
func (s FeedbackWorkloadScore) judged() (population, onTime, completed int, satisfied bool) {
	if m := s.Measured; m != nil {
		return m.Population, m.OnTime, m.OnTime + m.Late, m.Satisfied
	}
	return s.Population, s.OnTime, s.OnTime + s.Late, s.Satisfied
}

// shortfall is the completed rate a judged score shows for Workload w served
// short, when it shows one: the score missed w's SLO and completed only part
// of its population. A replayed score shows λ_w times the completed share, on
// time or late; the window's measurement shows the on-time rate the running
// set delivered (OnTimeRate), what the set did rather than a share of the
// planned demand. A remembered miss shows nothing: its rate is another
// window's, perhaps limited by that window's demand.
func (p *capacityPlanner) shortfall(w int, s FeedbackWorkloadScore) (float64, bool) {
	population, _, completed, satisfied := s.judged()
	switch m := s.Measured; {
	case satisfied || completed >= population:
		return 0, false
	case m == nil:
		return float64(completed) / float64(population) * p.ws[w].rate, true
	case m == p.ws[w].window:
		return m.OnTimeRate, true
	}
	return 0, false
}

// servingOption is the instance's serving option, without weight.
func servingOption(i SchedulerInstance) SchedulerServingShare {
	option := SchedulerServingShare{ProfileID: i.ProfileID, MaxBatch: i.MaxBatch}
	if i.Profile != nil {
		option.ProfileID = i.Profile.ID
	}
	return option
}

// instanceServing is the serving of routes over instances.
func instanceServing(instances []SchedulerInstance, routes map[string]int32) []SchedulerServingShare {
	return servingShares(routes, func(id string) (SchedulerServingShare, bool) {
		at := slices.IndexFunc(instances, func(i SchedulerInstance) bool { return i.ID == id })
		if at < 0 {
			return SchedulerServingShare{}, false
		}
		return servingOption(instances[at]), true
	})
}

// measurement is the fact that judges Workload w served as serving over
// population replayed requests, or nil when the replay judges it: the
// window's measurement when the result serves w as it runs (running) at a
// demand the window speaks for, else a covering remembered miss.
func (p *capacityPlanner) measurement(w int, running bool, serving []SchedulerServingShare, population int) *SchedulerMeasurement {
	ws := p.ws[w]
	if m := ws.window; running && m != nil && m.speaksFor(ws.rate, ws.observedRate) {
		return m
	}
	if population == 0 {
		return nil
	}
	// The on-time rate w's demand needs: the SLO rank of the population at the
	// planned rate.
	need := ws.rate * float64(empiricalSLORank(population, ws.d.SLO.Percentile)) / float64(population)
	for _, m := range ws.misses {
		if need > m.OnTimeRate && m.covers(serving) {
			// The result completes on time at most the rate the set delivered,
			// that share of its population.
			judged := m
			judged.Population, judged.OnTime = population, int(m.OnTimeRate*float64(population)/ws.rate)
			return &judged
		}
	}
	return nil
}
