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
// not part of it.
type SchedulerServingShare struct {
	ProfileID string `json:"profileID"`
	MaxBatch  int    `json:"maxBatch"`
	Weight    int32  `json:"weight,omitempty"`
}

// SchedulerMeasurement is how the running configuration served a Definition
// in the current load window, as its clients reported it.
//
// It holds the client-reported outcomes over the Definition's compute arrivals
// from Since to one SLO target before the end of the observation window, the
// arrivals that had the whole target to report: OnTime and Late are
// completions within and beyond the SLO target, every other report and every
// unreported arrival is a failure. OnTimeRate is its on-time completions per
// second of that stretch and Satisfied the empirical rank rule over
// Population. It judges every result that serves the Workload as it runs; the
// replay judges every other result.
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
	if !slices.EqualFunc(a, b, func(x, y SchedulerServingShare) bool {
		return x.ProfileID == y.ProfileID && x.MaxBatch == y.MaxBatch
	}) {
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

// windowMeasurement is d's measurement on the running serving over its load
// window up to one SLO target before end, [since, end - target), or nil when
// there is no window or it holds fewer reports than identify d's SLO percentile
// (MinimumServiceSamples). The window starts no earlier than the last action's
// end. It exists only while every action has ended and every routed instance of
// d is routable for it: otherwise the serving it would measure is not settled.
func windowMeasurement(snapshot SchedulerSnapshot, since, end time.Time, d *Definition, routes map[string]int32, serving []SchedulerServingShare) *SchedulerMeasurement {
	until := end.Add(-d.SLO.TargetLatency)
	if !schedulerActionsEnded(snapshot.Actions) {
		return nil
	}
	for _, a := range snapshot.Actions {
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
		if !a.computeInput() || a.Definition.UID != d.UID || a.At.Before(since) || !a.At.Before(until) {
			continue
		}
		switch {
		case a.ClientOutcome == outcomeCompleted && a.ClientElapsed <= a.Definition.SLO.TargetLatency:
			m.OnTime++
		case a.ClientOutcome == outcomeCompleted:
			m.Late++
		case a.ClientOutcome == "" && a.Outcome != "":
			// Finished at the server with its client's outcome unknown.
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
	m.OnTimeRate = float64(m.OnTime) / until.Sub(since).Seconds()
	m.Satisfied = m.OnTime >= empiricalSLORank(m.Population, d.SLO.Percentile)
	return &m
}

// schedulerActionsEnded reports whether no action is in flight.
func schedulerActionsEnded(actions []Action) bool {
	return !slices.ContainsFunc(actions, func(a Action) bool { return a.EndedAt == nil })
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
// planned demand.
func (p *capacityPlanner) shortfall(w int, s FeedbackWorkloadScore) (float64, bool) {
	population, _, completed, satisfied := s.judged()
	switch m := s.Measured; {
	case satisfied || completed >= population:
		return 0, false
	case m == nil:
		return float64(completed) / float64(population) * p.ws[w].rate, true
	default:
		return m.OnTimeRate, true
	}
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
