package serving

import (
	"cmp"
	"context"
	"maps"
	"reflect"
	"slices"
	"time"
)

const schedulerScaleDownStabilizing = "ScaleDownStabilizing"
const schedulerObservationGap = "ObservationGap"
const schedulerTempoSlow = "slow"
const schedulerFirstObservation = "FirstObservation"

// SchedulerDownState records demand protection separately from energy load.
type SchedulerDownState struct {
	Rate        float64       `json:"rate"`
	WindowMax   float64       `json:"windowMax"`
	WindowNanos time.Duration `json:"windowNanos"`
	Supply      float64       `json:"supply"`
	Allowed     bool          `json:"allowed"`
	Reason      string        `json:"reason"`
	ResetReason string        `json:"resetReason,omitempty"`
}

type schedulerDemandSample struct {
	at   time.Time
	rate float64
}
type schedulerDemandWindow struct {
	since   time.Time
	samples []schedulerDemandSample
}

func (w *schedulerDemandWindow) observe(at time.Time, rate float64, duration time.Duration) (float64, bool) {
	cutoff := at.Add(-duration)
	first := 0
	for first < len(w.samples) && !w.samples[first].at.After(cutoff) {
		first++
	}
	w.samples = append(w.samples[first:], schedulerDemandSample{at, rate})
	maximum := rate
	for _, sample := range w.samples {
		maximum = max(maximum, sample.rate)
	}
	return maximum, at.Sub(w.since) >= duration
}

func (p *capacityPlanner) tempoSupply(uid string) float64 {
	var supply float64
	for _, i := range p.snapshot.Instances {
		if i.WorkloadUID == uid && i.Ready && !i.Stopped && i.Routable[uid] && schedulerInstanceRouted(p.snapshot.Routing, i.ID) {
			k, _ := p.currentKappa(i)
			supply += k
		}
	}
	return supply
}

// SchedulerTempo belongs to one controller; restart deliberately loses history.
type SchedulerTempo struct {
	settings        SchedulerSettings
	at              time.Time
	windows         map[string]schedulerDemandWindow
	membershipSince time.Time
}

// Observe stores one effective-demand sample per normal controller observation.
func (t *SchedulerTempo) Observe(settings SchedulerSettings, s *SchedulerSnapshot, capacity []CapacityRow) {
	search := schedulerSearchPopulation(context.Background(), settings, *s)
	decision := SchedulerDecision{Search: search, Workloads: schedulerWorkloadStates(settings, *s)}
	p := newCapacityPlanner(context.Background(), settings, *s, SchedulerCandidate{}, capacity, decision)
	reset := t.windows == nil || !reflect.DeepEqual(t.settings, settings)
	gap := !t.at.IsZero() && (s.At.Before(t.at) || s.At.Sub(t.at) > settings.MaxObservationAge+settings.ControlInterval)
	normal := len(s.Reasons) == 0 && search.WindowEnd.Sub(search.WindowStart) >= min(settings.ObservationWindow, settings.ControlInterval)
	windows := make(map[string]schedulerDemandWindow, len(p.ws))
	states := make(map[string]SchedulerDownState, len(p.ws))
	sameUIDs := len(t.windows) == len(p.ws)
	for _, w := range p.ws {
		if _, ok := t.windows[w.d.UID]; !ok {
			sameUIDs = false
		}
	}
	// UID-set changes restart only the vector's observation coverage, not demand history.
	if reset || gap || !normal || !sameUIDs {
		t.membershipSince = s.At
	}
	all, strict := normal && len(p.ws) > 0, false
	for _, w := range p.ws {
		uid := w.d.UID
		window, exists := t.windows[uid]
		state := SchedulerDownState{Rate: w.rate, WindowNanos: settings.ScaleDownStabilization, Reason: schedulerScaleDownStabilizing}
		switch {
		case !exists:
			state.ResetReason = schedulerFirstObservation
		case gap || !normal:
			state.ResetReason = schedulerObservationGap
		case reset:
			state.ResetReason = "SettingsChanged"
		}
		if reset || gap || !normal || len(window.samples) == 0 {
			window = schedulerDemandWindow{since: s.At}
		}
		if normal {
			var complete bool
			state.WindowMax, complete = window.observe(s.At, w.rate, settings.ScaleDownStabilization)
			state.Supply = p.tempoSupply(uid)
			state.Allowed = complete && state.WindowMax < state.Supply
			if state.Allowed {
				state.Reason = "ScaleDownStabilized"
			} else if complete {
				state.Reason = "DemandNotBelowSupply"
			}
			all = all && complete && state.WindowMax <= state.Supply
			strict = strict || state.WindowMax < state.Supply
		} else {
			all = false
			state.Reason = "ScaleDownObservationGap"
		}
		windows[uid], states[uid] = window, state
	}
	s.ScaleDown = states
	s.EmptyNodeScaleDownAllowed = all && strict && s.At.Sub(t.membershipSince) >= settings.ScaleDownStabilization
	t.windows, t.at = windows, s.At
	t.settings = settings
	t.settings.AutonomousWorkloads = slices.Clone(settings.AutonomousWorkloads)
	t.settings.ExcludedNodes = slices.Clone(settings.ExcludedNodes)
	t.settings.MinimumDemand = maps.Clone(settings.MinimumDemand)
}

func schedulerSlowAllowed(s SchedulerSnapshot, uid string) bool {
	return s.ScaleDown[uid].Allowed
}
func schedulerEmptySlowAllowed(s SchedulerSnapshot) bool {
	return s.EmptyNodeScaleDownAllowed
}

// schedulerDownCapacity preserves evidence on returns before plan search.
func schedulerDownCapacity(settings SchedulerSettings, snapshot SchedulerSnapshot) *SchedulerCapacity {
	if len(snapshot.ScaleDown) == 0 {
		return nil
	}
	record := &SchedulerCapacity{Period: settings.ComparisonPeriod, Workloads: make([]SchedulerCapacityWorkload, 0, len(snapshot.ScaleDown))}
	for uid, down := range snapshot.ScaleDown {
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{UID: uid, Rate: down.Rate, MinimumDemand: settings.MinimumDemand[uid], ScaleDown: &down})
	}
	slices.SortFunc(record.Workloads, func(a, b SchedulerCapacityWorkload) int { return cmp.Compare(a.UID, b.UID) })
	return record
}
