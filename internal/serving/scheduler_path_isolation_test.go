package serving

import (
	"context"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func pathIsolationProof(rows []CapacityRow, uid string) {
	for j := range rows {
		if rows[j].Input.WorkloadUID == uid {
			zero := 0.0
			rows[j].Kappa = &zero
			rows[j].Reason = CapacityServiceExceedsSLO
			rows[j].MinimumServiceNanos = float64(20 * time.Millisecond)
		}
	}
}

func TestPathInfeasibleRequiresEverySourceVariantProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*SchedulerSnapshot, *[]CapacityRow)
		want   string
	}{
		{name: "all variants", want: schedulerPathServiceExceedsSLO},
		{name: "path alone", mutate: func(snap *SchedulerSnapshot, rows *[]CapacityRow) {
			snap.PathBudgets[0].TotalMS = 110
			for j := range *rows {
				(*rows)[j].Input.CompletionMarginMS = 110
				(*rows)[j].Key = (*rows)[j].Input.Key()
			}
		}, want: schedulerPathBudgetExceedsSLO},
		{name: "missing source allocation", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { *rows = (*rows)[:2] }},
		{name: "missing batch variant", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { *rows = (*rows)[1:] }},
		{name: "pending", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].Status = CapacityPending }},
		{name: "rejected with numeric zero", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].Status = CapacityRejected }},
		{name: "missing kappa", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].Kappa = nil }},
		{name: "grid zero", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].Reason = "" }},
		{name: "positive kappa", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { k := 1.0; (*rows)[3].Kappa = &k }},
		{name: "NaN kappa", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { k := math.NaN(); (*rows)[3].Kappa = &k }},
		{name: "missing minimum", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].MinimumServiceNanos = 0 }},
		{name: "infinite minimum", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].MinimumServiceNanos = math.Inf(1) }},
		{name: "NaN minimum", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) { (*rows)[3].MinimumServiceNanos = math.NaN() }},
		{name: "fits boundary", mutate: func(_ *SchedulerSnapshot, rows *[]CapacityRow) {
			(*rows)[3].MinimumServiceNanos = float64(10 * time.Millisecond)
		}},
		{name: "stale budget key", mutate: func(snap *SchedulerSnapshot, _ *[]CapacityRow) { snap.PathBudgets[0].TotalMS = 91 }},
		{name: "empty universe", mutate: func(snap *SchedulerSnapshot, _ *[]CapacityRow) { snap.Options = nil }},
		{name: "empty variants", mutate: func(snap *SchedulerSnapshot, _ *[]CapacityRow) { snap.Options[1].Instance.MaxBatch = 0 }},
		{name: "no compatible source", mutate: func(snap *SchedulerSnapshot, _ *[]CapacityRow) {
			for j := range snap.Options {
				snap.Options[j].CompatibleUIDs = []string{"another-uid"}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, snap := schedulerFixture()
			w := snap.Workloads[0]
			snap.PathBudgets = []PathBudget{{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: 90, PostMS: 30, JudgmentPercentile: 100}}
			profile := *snap.Options[0].Instance.Profile
			profile.Batches = []BatchProfile{
				schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 1}, []float64{float64(20 * time.Millisecond)}),
				schedulerTestBatchProfile(BatchProfile{Batch: 2, EnergyPerInputJoules: 1}, []float64{float64(20 * time.Millisecond)}),
			}
			snap.Options[0].Instance.Profile, snap.Options[0].Instance.MaxBatch = &profile, 2
			other := snap.Options[0]
			other.Instance.Allocation.MemoryBytes++ // Same profile, distinct actual row input.
			snap.Options = append(snap.Options, other)
			rows := capacityRowsForTest(s, snap, nil)
			pathIsolationProof(rows, w.UID)
			if tc.mutate != nil {
				tc.mutate(&snap, &rows)
			}
			p := &capacityPlanner{settings: s, snapshot: snap, rows: map[string]CapacityRow{}}
			for _, row := range rows {
				p.rows[row.Key] = row
			}
			if got := p.pathInfeasibleReason(w); got != tc.want {
				t.Fatalf("path infeasibility = %q, want %q", got, tc.want)
			}
		})
	}
}

// One impossible physical instance and two independently controllable replicas.
func pathIsolationFixture() (SchedulerSettings, SchedulerSnapshot, []CapacityRow) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	s.MaxInstances, s.MaxReplicas = 3, 2
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	w1, w2 := snap.Workloads[0], poolsAddWorkload(&snap, "w2")
	s.AutonomousWorkloads = append(s.AutonomousWorkloads, w2.UID)
	snap.PathBudgets = []PathBudget{{WorkloadUID: w1.UID, DefinitionID: w1.Identity(), Source: "observed", TotalMS: 90, PostMS: 30, JudgmentPercentile: 100}}
	for _, uid := range []string{"n2", "n3"} {
		node := poolsAddNode(&snap, uid, map[string]float64{"fixed": 10})
		i := poolsInstance(snap.Instances[0], "i-"+uid, node, "fixed", w2.UID)
		profile := *i.Profile
		profile.IdleBaselineWatts = 20
		i.Profile = &profile
		snap.Instances = append(snap.Instances, i)
		snap.Options = append(snap.Options, poolsOption(i))
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionRemove, node, schedulerState(i), "unloaded", "fixed"))
	}
	snap.Routing = map[string]map[string]int32{w1.UID: {"i1": 1000}, w2.UID: {"i-n2": 500, "i-n3": 500}}
	capacityLoad(s, &snap, w1, 10)
	capacityLoad(s, &snap, w2, 10)
	snap.Instances[0].Profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 1, MemoryBytes: 100}, []float64{float64(20 * time.Millisecond)}),
	}
	rows := capacityRowsForTest(s, snap, nil)
	pathIsolationProof(rows, w1.UID)
	return s, snap, rows
}

func TestPathIsolationAllowsIndependentRetire(t *testing.T) {
	for _, strategy := range []string{protocol.V1, protocol.V2} {
		t.Run(strategy, func(t *testing.T) {
			s, snap, rows := pathIsolationFixture()
			s.PlanSearchStrategy = strategy
			d := capacityDecideForTest(t, s, snap, rows)
			if d.Target == nil || len(d.Target.Instances) != 2 || !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool {
				return reflect.DeepEqual(i, snap.Instances[0])
			}) || !maps.Equal(d.Target.Routing["w1"], snap.Routing["w1"]) || len(d.Target.Routing["w2"]) != 1 {
				t.Fatalf("must freeze w1 and retire one w2 replica: %+v", d)
			}
			if slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Node == snap.Instances[0].Node }) {
				t.Fatalf("fixed physical node changed: %+v", d.Target.Actions)
			}
			if !slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove }) {
				t.Fatalf("independent retire missing: %+v", d.Target.Actions)
			}
			if d.Capacity == nil || len(d.Capacity.Workloads) != 1 || d.Capacity.Workloads[0].UID != "w2" {
				t.Fatalf("isolated UID entered controlled feasibility: %+v", d.Capacity)
			}

			// Fixed nodes stay in physical occupancy and keep their loaded baseline.
			ctx := context.Background()
			keep := SchedulerCandidate{Nodes: schedulerCommonNodes(snap), Instances: snap.Instances, Routing: snap.Routing}
			state := SchedulerDecision{Search: schedulerSearchPopulation(ctx, s, snap), Workloads: schedulerWorkloadStates(s, snap)}
			p := newCapacityPlanner(ctx, s, snap, keep, rows, state)
			p.fixCapacityWorkloads(&state)
			if err := p.build(); err != nil {
				t.Fatal(err)
			}
			n := slices.IndexFunc(p.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == snap.Instances[0].Node })
			fixed := p.plans[n][0]
			if !p.fixed(snap.Instances[0]) || fixed.others != 1 || fixed.before != snap.Instances[0].Profile.IdleBaselineWatts ||
				fixed.energy(0, s.ComparisonPeriod.Seconds()) != fixed.before*s.ComparisonPeriod.Seconds() {
				t.Fatalf("physical occupancy or fixed loaded energy lost: %+v", fixed)
			}
		})
	}
}

func TestPathGridZeroRemainsControlled(t *testing.T) {
	for _, strategy := range []string{protocol.V1, protocol.V2} {
		t.Run(strategy, func(t *testing.T) {
			s, snap, rows := pathIsolationFixture()
			s.PlanSearchStrategy = strategy
			for j := range rows {
				if rows[j].Input.WorkloadUID == "w1" {
					rows[j].Reason = ""
				}
			}
			d := capacityDecideForTest(t, s, snap, rows)
			state := slices.IndexFunc(d.Workloads, func(w SchedulerWorkloadState) bool { return w.UID == "w1" })
			if state < 0 || d.Workloads[state].State != WorkloadControlled || d.Capacity == nil || len(d.Capacity.Workloads) != 2 {
				t.Fatalf("numeric zero must remain controlled: %+v", d)
			}
		})
	}
}
