package serving

import (
	"context"
	"slices"
)

// SchedulerReductionWorkloads freezes which live supplies an admitted target
// retires. A later demand spike must not reclassify that target as recovery.
// Failed/terminal sources and their cleanup are not live-supply reductions.
func SchedulerReductionWorkloads(settings SchedulerSettings, snapshot SchedulerSnapshot, rows []CapacityRow, target SchedulerCandidate) []string {
	var uids []string
	for _, old := range snapshot.Instances {
		if !old.Ready || old.Stopped || old.Terminated {
			continue
		}
		uid := old.WorkloadUID
		if uid == "" {
			uid = old.OwnerWorkloadUID
		}
		if uid == "" || slices.Contains(uids, uid) {
			continue
		}
		_, scoped := target.Routing[uid]
		if !scoped && !slices.ContainsFunc(target.Actions, func(a Action) bool {
			return a.Instance == old.ID || a.Kind == schedulerActionPower && a.Node == old.Node
		}) {
			continue
		}
		next := slices.IndexFunc(target.Instances, func(i SchedulerInstance) bool { return i.ID == old.ID })
		reduced := next < 0
		if next >= 0 {
			i := target.Instances[next]
			if i.MaxBatch != old.MaxBatch || i.MaxWait != old.MaxWait {
				before, oldOK := rowKappa(settings, snapshot, rows, old, uid)
				after, newOK := rowKappa(settings, snapshot, rows, i, uid)
				reduced = reduced || !oldOK || !newOK || after <= before
			}
		}
		for _, action := range target.Actions {
			reduced = reduced || (action.Kind == schedulerActionStop || action.Kind == schedulerActionRemove) && action.Instance == old.ID || action.Kind == schedulerActionPower && action.Node == old.Node
		}
		if reduced {
			uids = append(uids, uid)
		}
	}
	slices.Sort(uids)
	return uids
}

// rowKappa reads the built kappa of instance variant i for Workload uid straight
// from the rows; a missing, pending or rejected row counts as no supply.
func rowKappa(settings SchedulerSettings, snapshot SchedulerSnapshot, rows []CapacityRow, i SchedulerInstance, uid string) (float64, bool) {
	var d *Definition
	for _, w := range snapshot.Workloads {
		if w != nil && w.UID == uid {
			d = w
		}
	}
	if d == nil {
		return 0, false
	}
	key := NewCapacityInput(settings, snapshot.DispatchOccupancy, SchedulerOption{Instance: i}, d).Key()
	for _, row := range rows {
		if row.Key == key {
			if row.Status == CapacityPending || row.Kappa == nil {
				return 0, false
			}
			return *row.Kappa, true
		}
	}
	return 0, false
}

// SchedulerReductionCommit validates only a frozen target, never searches for a
// new candidate. It shares admission/idle protection and the planner's integer
// weight and backlog/SLO judgments; unrouted prepared capacity supplies nothing.
func SchedulerReductionCommit(settings SchedulerSettings, snapshot SchedulerSnapshot, rows []CapacityRow, target SchedulerCandidate, uids []string) string {
	if len(uids) == 0 {
		return ""
	}
	if len(snapshot.Reasons) != 0 || len(schedulerValidateObservationTimes(&snapshot, settings.MaxObservationAge)) != 0 || snapshot.At.Sub(snapshot.ObservationEnd) > settings.MaxObservationAge {
		return schedulerWindowCapacityInsufficient
	}
	decision := SchedulerDecision{Search: schedulerSearchPopulation(context.Background(), settings, snapshot)}
	for _, uid := range uids {
		if !slices.ContainsFunc(snapshot.Workloads, func(d *Definition) bool { return d != nil && d.UID == uid }) {
			return schedulerWindowCapacityInsufficient
		}
		if _, ok := snapshot.ScaleDown[uid]; !ok {
			return schedulerWindowCapacityInsufficient
		}
		decision.Workloads = append(decision.Workloads, SchedulerWorkloadState{UID: uid, State: WorkloadControlled})
	}
	if decision.Search.WindowEnd.Sub(decision.Search.WindowStart) < min(settings.ObservationWindow, settings.ControlInterval) {
		return schedulerWindowCapacityInsufficient
	}
	p := newCapacityPlanner(context.Background(), settings, snapshot, SchedulerCandidate{}, rows, decision)
	for _, w := range p.ws {
		demand, protected := p.protectedReductionDemand(w)
		items := []capacityItem{}
		for id, weight := range target.Routing[w.d.UID] {
			if weight <= 0 {
				continue
			}
			at := slices.IndexFunc(target.Instances, func(i SchedulerInstance) bool { return i.ID == id })
			if at < 0 {
				return schedulerWindowCapacityInsufficient
			}
			i := target.Instances[at]
			kappa, _, reason := p.kappa(SchedulerOption{Instance: i}, 0)
			if reason != "" || i.Terminated || i.WorkloadUID != w.d.UID || kappa <= 0 {
				return schedulerWindowCapacityInsufficient
			}
			items = append(items, capacityItem{id: id, kappa: kappa, weight: weight})
		}
		weights, _, allowed := capacityFixedWeights(protected, items)
		kappa, margin := capacityPositiveSupply(items, weights, protected, demand.rate)
		if !allowed || !margin || !capacityJudge(demand, kappa, kappa, kappa > 0, 0).satisfied {
			return schedulerWindowCapacityInsufficient
		}
	}
	return ""
}
