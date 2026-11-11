package serving

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// These calls pass through the same ownership/native projection the planner consumes.
func backlogRuntimeForTest(snap SchedulerSnapshot, phase string, count int, expired bool) (*Runtime, *runtimeInstance) {
	d, id := snap.Workloads[0], snap.Instances[0].ID
	owner := &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: id, Ready: true, ObservedAt: snap.At}, Capacity: count}, outstanding: count}
	r := &Runtime{calls: map[string]*Call{}, instances: map[string]*runtimeInstance{id: owner}}
	for n := range count {
		rid := fmt.Sprintf("backlog-%d", n)
		deadline := snap.At.Add(time.Second)
		if expired {
			deadline = snap.At.Add(-time.Millisecond)
		}
		c := &Call{receipt: Receipt{ID: rid, UID: d.UID, ReceivedAt: snap.At.Add(-time.Millisecond), Deadline: deadline}, definition: d, identity: d.Identity()}
		if phase != phasePending {
			c.assignment, c.instance = &Assignment{Instance: id}, owner
			if phase == phaseReturning {
				c.backendDone = true
				c.exchangeFinishedAt = snap.At.Add(-time.Millisecond)
			} else if phase != phaseAssigned {
				if owner.State.Native == nil {
					owner.State.Native = &firstfloor.State{Ready: true}
				}
				owner.State.Native.Requests = append(owner.State.Native.Requests, firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: rid, WorkloadUID: d.UID, Definition: d.Identity()}, Phase: phase, QueueMS: 1})
			}
		}
		r.calls[rid] = c
	}
	return r, owner
}

func TestPlannerBacklogProjectedPhases(t *testing.T) {
	for _, phase := range []string{phasePending, phaseAssigned, phaseQueued, phaseDeferred, phaseRunning, phaseReturning} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%v", phase, expired), func(t *testing.T) {
				s, snap := schedulerFixture()
				r, owner := backlogRuntimeForTest(snap, phase, 1, expired)
				snap.Outstanding = r.SchedulerInputs(func() time.Time { return snap.At }).Outstanding
				decision := SchedulerDecision{Workloads: []SchedulerWorkloadState{{UID: snap.Workloads[0].UID, State: WorkloadControlled}}}
				decision.Search.WindowStart, decision.Search.WindowEnd = snap.At.Add(-time.Minute), snap.At
				p := newCapacityPlanner(context.Background(), s, snap, SchedulerCandidate{}, decision)
				want := 1
				if expired || phase == phaseRunning || phase == phaseReturning {
					want = 0
				}
				if len(snap.Outstanding) != 1 || p.ws[0].queued != want || p.ws[0].rate != float64(want)/60 {
					t.Fatalf("phase=%s q=%d rate=%g want=%d inventory=%+v", phase, p.ws[0].queued, p.ws[0].rate, want, snap.Outstanding)
				}
				if phase != phasePending {
					route := inputRoute{entries: []routeEntry{{instance: owner}}, weights: []int32{1}, current: []int64{0}, available: []bool{false}}
					if owner.outstanding != 1 || r.chooseLocked(route, &Call{definition: snap.Workloads[0]}, snap.At).entry != nil {
						t.Fatal("planner released physical capacity")
					}
				}
			})
		}
	}
}

func TestPlannerNativeQueueUsesFiniteDemandSLO(t *testing.T) {
	for _, phase := range []string{phaseQueued, phaseDeferred} {
		t.Run(phase, func(t *testing.T) {
			s, snap := schedulerFixture()
			snap.Workloads[0].SLO.TargetLatency = 20 * time.Millisecond
			r, owner := backlogRuntimeForTest(snap, phase, 3, false)
			snap.Outstanding = r.SchedulerInputs(func() time.Time { return snap.At }).Outstanding
			decision := SchedulerDecision{Workloads: []SchedulerWorkloadState{{UID: snap.Workloads[0].UID, State: WorkloadControlled}}}
			p := newCapacityPlanner(context.Background(), s, snap, SchedulerCandidate{}, decision)
			snap.Arrivals = nil
			keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances, Routing: schedulerInstanceRouting(context.Background(), snap.Workloads, snap.Instances)}
			result, err := EvaluateFeedbackCandidate(context.Background(), s, snap, keep, 0)
			if err != nil {
				t.Fatal(err)
			}
			if p.ws[0].queued != 3 || owner.outstanding != 3 || len(result.Workloads) != 1 || result.Workloads[0].Population != 3 || result.Workloads[0].Satisfied {
				t.Fatalf("native queue lost finite-demand obligation: q=%d slots=%d result=%+v", p.ws[0].queued, owner.outstanding, result)
			}
		})
	}
}

func TestPlannerRunningAndExpiredOwnershipDoNotTriggerExpansion(t *testing.T) {
	for _, tc := range []struct {
		name, phase     string
		count           int
		expired, expand bool
	}{
		{name: "running", phase: phaseRunning, count: 1},
		{name: "pending", phase: phasePending, count: 1, expand: true},
		{name: "unobserved assigned", phase: phaseAssigned, count: 1, expand: true},
		{name: "81 expired assigned", phase: phaseAssigned, count: 81, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
			snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
			d := snap.Workloads[0]
			d.SLO.TargetLatency = time.Second
			n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
			i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", d.UID)
			snap.Options = append(snap.Options, poolsOption(i2))
			snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(i2), "fixed")}
			capacityLoad(s, &snap, d, .1)
			r, owner := backlogRuntimeForTest(snap, tc.phase, tc.count, tc.expired)
			snap.Outstanding = r.SchedulerInputs(func() time.Time { return snap.At }).Outstanding
			got := capacityDecideForTest(t, s, snap)
			expanded := got.Target != nil && poolsOn(*got.Target, n2)
			wantQueued := 0
			if tc.expand {
				wantQueued = 1
			}
			if expanded && !tc.expand || got.Capacity == nil || got.Capacity.Workloads[0].Queued != wantQueued {
				t.Fatalf("backlog/expansion=%v status=%s reasons=%v capacity=%+v", expanded, got.Status, got.Reasons, got.Capacity)
			}
			if tc.expired {
				score := got.Keep.Feedback[0]
				if score.Timeout != tc.count || score.Satisfied || score.Population < tc.count {
					t.Fatalf("expired ownership lost its finite failure outcomes: %+v", score)
				}
			}
			if owner.outstanding != tc.count || len(snap.Outstanding) != tc.count {
				t.Fatal("ownership was removed")
			}
		})
	}
}

// NOTE: cleanup is not atomic with snapshot projection. This retained pending
// record can exist between cancellation/dispatch expiry and response recording.
func TestPlannerExpiredPendingResponseGap(t *testing.T) {
	s, snap := schedulerFixture()
	r, _ := backlogRuntimeForTest(snap, phasePending, 1, true)
	for _, c := range r.calls {
		c.backendDone, c.transportDone = true, true
	}
	snap.Outstanding = r.SchedulerInputs(func() time.Time { return snap.At }).Outstanding
	decision := SchedulerDecision{Workloads: []SchedulerWorkloadState{{UID: snap.Workloads[0].UID, State: WorkloadControlled}}}
	p := newCapacityPlanner(context.Background(), s, snap, SchedulerCandidate{}, decision)
	if len(snap.Outstanding) != 1 || snap.Outstanding[0].Phase != phasePending || p.ws[0].queued != 0 {
		t.Fatalf("expired pending counted: %+v", snap.Outstanding)
	}
}
