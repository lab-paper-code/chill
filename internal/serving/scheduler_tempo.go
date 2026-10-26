package serving

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"time"
)

const schedulerScaleDownStabilizing = "ScaleDownStabilizing"
const schedulerObservationGap = "ObservationGap"
const schedulerTempoSlow = "slow"

// SchedulerDownState is the decision evidence for the common slow-candidate gate.
type SchedulerDownState struct {
	Baseline    float64       `json:"baselineRate"`
	Previous    float64       `json:"previousRate"`
	Rate        float64       `json:"rate"`
	DownSince   *time.Time    `json:"downSince,omitempty"`
	Elapsed     time.Duration `json:"elapsedNanos"`
	Allowed     bool          `json:"allowed"`
	Reason      string        `json:"reason"`
	ResetReason string        `json:"resetReason,omitempty"`
}

// SchedulerTempo belongs to one controller. Restarting deliberately loses timers.
type SchedulerTempo struct {
	settings    SchedulerSettings
	at          time.Time
	states      map[string]SchedulerDownState
	globalSince *time.Time
}

// Observe uses the planner's effective demand, including queued and minimum demand.
func (t *SchedulerTempo) Observe(settings SchedulerSettings, s *SchedulerSnapshot) {
	search := schedulerSearchPopulation(context.Background(), settings, *s)
	decision := SchedulerDecision{Search: search, Workloads: schedulerWorkloadStates(settings, *s)}
	p := newCapacityPlanner(context.Background(), settings, *s, SchedulerCandidate{}, nil, decision)
	reset := t.states == nil || !reflect.DeepEqual(t.settings, settings)
	gap := !t.at.IsZero() && (s.At.Before(t.at) || s.At.Sub(t.at) > settings.MaxObservationAge+settings.ControlInterval)
	normal := len(s.Reasons) == 0 && search.WindowEnd.Sub(search.WindowStart) >= min(settings.ObservationWindow, settings.ControlInterval)
	states := make(map[string]SchedulerDownState, len(p.ws))
	anyDown := false
	for _, w := range p.ws {
		old, exists := t.states[w.d.UID]
		state := old
		state.Previous, state.Rate, state.Elapsed, state.Allowed = old.Rate, w.rate, 0, false
		state.Reason = schedulerScaleDownStabilizing
		state.ResetReason = ""
		switch {
		case !exists:
			state.Baseline, state.DownSince = w.rate, nil
			state.Reason = "ScaleDownFirstObservation"
			state.ResetReason = "FirstObservation"
		case !normal:
			state.DownSince = nil
			state.Reason = "ScaleDownObservationGap"
			state.ResetReason = schedulerObservationGap
		default:
			if reset || gap || w.rate > old.Rate {
				state.DownSince = nil
				state.Reason = "ScaleDownRestarted"
				switch {
				case gap:
					state.ResetReason = schedulerObservationGap
				case reset:
					state.ResetReason = "SettingsChanged"
				default:
					state.ResetReason = "DemandIncreased"
				}
			}
			if w.rate >= old.Baseline {
				state.Baseline, state.DownSince = w.rate, nil
				state.Reason = "DemandNotDecreasing"
			} else {
				if state.DownSince == nil {
					at := s.At
					state.DownSince = &at
				}
				state.Elapsed = s.At.Sub(*state.DownSince)
				state.Allowed = state.Elapsed >= settings.ScaleDownStabilization
				if state.Allowed {
					state.Reason = "ScaleDownStabilized"
				}
			}
		}
		anyDown = anyDown || state.DownSince != nil
		states[w.d.UID] = state
	}
	t.observeVector(s, states, normal, reset || gap, anyDown, settings.ScaleDownStabilization)
	t.states, t.at = states, s.At
	t.settings = settings
	t.settings.AutonomousWorkloads = slices.Clone(settings.AutonomousWorkloads)
	t.settings.ExcludedNodes = slices.Clone(settings.ExcludedNodes)
	t.settings.MinimumDemand = maps.Clone(settings.MinimumDemand)
}

// observeVector never offsets one Workload's rise with another's decline.
func (t *SchedulerTempo) observeVector(s *SchedulerSnapshot, states map[string]SchedulerDownState, normal, reset, anyDown bool, stabilization time.Duration) {
	if reset {
		t.globalSince = nil
	}
	allDown := normal && len(states) == len(t.states) && len(states) > 0
	for uid, state := range states {
		old, ok := t.states[uid]
		if !ok || state.Rate > old.Rate {
			allDown = false
		}
	}
	if !allDown || !anyDown {
		t.globalSince = nil
	} else if t.globalSince == nil {
		at := s.At
		t.globalSince = &at
	}
	s.ScaleDown = maps.Clone(states)
	s.EmptyNodeScaleDownAllowed = stabilization == 0 || t.globalSince != nil && s.At.Sub(*t.globalSince) >= stabilization
}

// Selected rebases only Workloads whose physical or policy selection changed.
func (t *SchedulerTempo) Selected(d SchedulerDecision) {
	if d.Target == nil || d.Target.Keep {
		return
	}
	for uid, state := range t.states {
		changed := false
		for _, i := range d.Target.Instances {
			if i.WorkloadUID == uid && !slices.ContainsFunc(d.Keep.Instances, func(old SchedulerInstance) bool {
				return old.ID == i.ID && old.MaxBatch == i.MaxBatch && old.MaxWait == i.MaxWait
			}) {
				changed = true
			}
		}
		for _, old := range d.Keep.Instances {
			if old.WorkloadUID == uid && !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == old.ID }) {
				changed = true
			}
		}
		if changed {
			state.Baseline, state.DownSince, state.Elapsed, state.Allowed = state.Rate, nil, 0, false
			t.states[uid] = state
		}
	}
}

func schedulerSlowAllowed(settings SchedulerSettings, s SchedulerSnapshot, uid string) bool {
	return settings.ScaleDownStabilization == 0 || s.ScaleDown[uid].Allowed
}
func schedulerEmptySlowAllowed(settings SchedulerSettings, s SchedulerSnapshot) bool {
	return settings.ScaleDownStabilization == 0 || s.EmptyNodeScaleDownAllowed
}
