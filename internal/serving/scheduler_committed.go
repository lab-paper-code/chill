package serving

import (
	"context"
	"maps"
	"slices"
	"time"
)

// schedulerKeepRouting is the routing the running configuration serves: each
// instance's own weight, replaced by the snapshot's acknowledged routes.
func schedulerKeepRouting(ctx context.Context, snapshot SchedulerSnapshot) map[string]map[string]int32 {
	routing := schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)
	if routing == nil {
		return nil
	}
	for uid, routes := range snapshot.Routing {
		routing[uid] = routes
	}
	return routing
}

// schedulerNodeInMode reports a Node already in the power mode profile was
// measured in: until its power change completes, the instance preparing on it
// is not serving capacity the system has committed.
func schedulerNodeInMode(snapshot SchedulerSnapshot, node NodeReference, profile *Profile) bool {
	at := slices.IndexFunc(snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
	return at >= 0 && profile != nil && snapshot.Nodes[at].PowerMode == profile.Condition.PowerMode
}

// schedulerCommittedView presents the snapshot as if every reserved target had
// taken effect on the instances it is still preparing: each becomes Ready as
// its target instance (warmed now, with no arrival history), and each Workload
// whose target routes only Ready instances takes the target's routes. A
// preparing instance is capacity the system already committed, so a decision
// does not prepare a second one for the shortfall it covers. The reservations
// stay, so their Nodes remain unplannable. Reservations come in launch order:
// of two targets routing the same Workload, the later launched wins. committed
// lists the preparing instances the view counts as Ready, by ID.
func schedulerCommittedView(snapshot SchedulerSnapshot) (view SchedulerSnapshot, committed []string) {
	view = snapshot
	view.Instances = slices.Clone(snapshot.Instances)
	rename := schedulerCommitPreparing(snapshot, view.Instances)
	if len(rename) == 0 {
		return snapshot, nil
	}
	for _, id := range rename {
		committed = append(committed, id)
	}
	slices.Sort(committed)
	committed = slices.Compact(committed)
	view.replayScope = nil // the prediction authority of the view's instances, not of the preparing ones
	schedulerCommitRouting(snapshot, &view, rename)
	view.Nodes = slices.Clone(snapshot.Nodes)
	for k, n := range view.Nodes {
		if !slices.ContainsFunc(view.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node && !i.Terminated && !i.Ready }) {
			view.Nodes[k].Reasons = slices.DeleteFunc(slices.Clone(n.Reasons), func(r string) bool { return r == NodeInstanceUnavailable })
		}
	}
	return view, committed
}

// schedulerCommitPreparing replaces, in instances, each preparing instance a
// reserved target's instance is waiting on with that target instance made
// Ready, and returns target instance ID -> the instance preparing it.
func schedulerCommitPreparing(snapshot SchedulerSnapshot, instances []SchedulerInstance) map[string]string {
	rename := map[string]string{}
	for _, r := range snapshot.Reservations {
		if r.Target == nil {
			continue
		}
		for k, i := range instances {
			if i.Ready || i.Terminated {
				continue
			}
			for _, want := range r.Target.Instances {
				if want.ID != i.ID && want.ID != i.TargetID || !schedulerNodeInMode(snapshot, want.Node, want.Profile) {
					continue
				}
				committed := want
				committed.ID, committed.PodUID, committed.PodName, committed.TargetID = i.ID, i.PodUID, i.PodName, i.TargetID
				committed.Ready, committed.Stopped, committed.Terminated = true, false, false
				committed.ObservedAt, committed.NativeSpanAt = snapshot.At, time.Time{}
				committed.LastServiceEndAgeNanos, committed.LastServiceEndAgeUpperNanos, committed.AdmissionIdleUnknown = new(int64), nil, false
				committed.ArrivalAgesMS = map[string][]float64{}
				instances[k], rename[want.ID] = committed, i.ID
			}
		}
	}
	return rename
}

// schedulerCommitRouting gives each Workload whose reserved target routes only
// Ready instances of view the target's routes, and points the instances at
// the Workload they now serve.
func schedulerCommitRouting(snapshot SchedulerSnapshot, view *SchedulerSnapshot, rename map[string]string) {
	view.Routing = maps.Clone(snapshot.Routing)
	if view.Routing == nil {
		view.Routing = map[string]map[string]int32{}
	}
	for _, r := range snapshot.Reservations {
		if r.Target == nil {
			continue
		}
		for uid, routes := range r.Target.Routing {
			next, ready := map[string]int32{}, true
			for id, weight := range routes {
				if weight <= 0 {
					continue
				}
				if actual, ok := rename[id]; ok {
					id = actual
				}
				ready = ready && slices.ContainsFunc(view.Instances, func(i SchedulerInstance) bool { return i.ID == id && i.Ready && !i.Stopped })
				next[id] = weight
			}
			if ready && len(next) > 0 {
				view.Routing[uid] = next
			}
		}
	}
	for k, i := range view.Instances {
		for uid, routes := range view.Routing {
			if weight := routes[i.ID]; weight > 0 {
				view.Instances[k].WorkloadUID, view.Instances[k].Weight = uid, weight
			} else if i.WorkloadUID == uid && !maps.Equal(routes, snapshot.Routing[uid]) {
				view.Instances[k].WorkloadUID, view.Instances[k].Weight = "", 0
			}
		}
	}
}
