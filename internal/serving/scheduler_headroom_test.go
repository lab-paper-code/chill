package serving

import (
	"context"
	"math"
	"testing"
)

// Raw decision inputs: control-222-full/obs/20261030T12/scheduler.jsonl.gz
// S419, S607, S714 and S6056. These are admission counterfactuals, not
// execution replays: catalog/bridge effects are tested by the existing DAG suite.
func reductionAdmissionFixture(rate, supply, target float64) *capacityPlanner {
	uid := "w"
	old := SchedulerInstance{ID: "old", WorkloadUID: uid, Ready: true, Routable: map[string]bool{uid: true}}
	next := SchedulerInstance{ID: "next", WorkloadUID: uid}
	p := &capacityPlanner{ctx: context.Background(), period: 600,
		settings: SchedulerSettings{Controls: SchedulerControls{Routing: true}, ScaleDownHeadroomFraction: .25},
		snapshot: SchedulerSnapshot{Nodes: []SchedulerNode{{}}, Instances: []SchedulerInstance{old}},
		ws:       []capacityWorkload{{d: &Definition{UID: uid}, rate: rate, slack: 1}}, compared: []bool{false},
		keep: SchedulerCandidate{Routing: map[string]map[string]int32{uid: {"old": 1000}}},
	}
	p.currentCapacity = map[capacityInstanceKey]capacityItem{currentCapacityKey(old): {kappa: supply}}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{old}, items: [][]capacityItem{{{id: "old", kappa: supply, e: 1}}}, retained: [][]capacityItem{{{id: "old", kappa: supply, weight: 1000}}}}
	nextPlan := &capacityPlan{reduces: true, instances: []SchedulerInstance{next}, items: [][]capacityItem{{{id: "next", kappa: target, e: 1}}}, retained: [][]capacityItem{nil}}
	p.plans = [][]*capacityPlan{{keep, nextPlan}}
	return p
}

func TestTempoE53E55DownscaleHeadroomSCH55(t *testing.T) {
	for _, row := range []struct {
		name                 string
		rate, supply, target float64
	}{
		{"E5-3-S419", 54.333333333333336, 99.64337119287221, 64.28501986002459},
		{"E5-5-current", 31.400000000000002, 91.90714633483302, 39},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := reductionAdmissionFixture(row.rate, row.supply, row.target)
			p.ws[0].queued = 2
			if row.name == "E5-3-S419" {
				p.ws[0].queued = 4
			}
			result := p.evaluate([]int{1}, 0)
			if p.admits(&result) || result.feasible {
				t.Fatalf("headroom downscale admitted: %+v", result)
			}
			p.settings.ScaleDownHeadroomFraction = 0
			result = p.evaluate([]int{1}, 0)
			if !p.admits(&result) {
				t.Fatalf("h=0 changed previous admission: %+v", result)
			}
		})
	}
}

func TestTempoE53ProvenRecoverySCH56(t *testing.T) {
	for _, row := range []struct {
		name   string
		rate   float64
		queued int
	}{
		{"S607", 71.86666666666666, 22}, {"S714", 75.2, 43},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := reductionAdmissionFixture(row.rate, 64.285020, 100)
			p.ws[0].queued = row.queued
			result := p.evaluate([]int{1}, 0)
			if !p.admits(&result) || !result.feasible {
				t.Fatalf("proven recovery was excluded: %+v", result)
			}
			p.plans[0][1].items[0][0].kappa = 70
			result = p.evaluate([]int{1}, 0)
			if p.admits(&result) {
				t.Fatal("partial improvement admitted as recovery")
			}
			p.plans[0][1].items[0][0].kappa = 100
			p.settings.TransitionReserveFraction = .1
			result = p.evaluate([]int{1}, 0)
			if p.admits(&result) || result.exclusion != schedulerTransitionReserveInsufficient {
				t.Fatalf("recovery bypassed actual reserve: %+v", result)
			}
		})
	}
}

func TestTempoHeadroomSettingsBoundsSCH55(t *testing.T) {
	for _, h := range []float64{0, 1, .25, -.01, 1.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s, _ := schedulerFixture()
		s.ScaleDownHeadroomFraction = h
		valid := schedulerFinite(h) && h >= 0 && h <= 1
		if (s.Validate() == nil) != valid {
			t.Fatalf("h=%g validation mismatch", h)
		}
	}
}

func TestTempoHeadroomPositiveWeightsSCH55(t *testing.T) {
	p := reductionAdmissionFixture(10, 100, 20)
	p.settings.ScaleDownHeadroomFraction = 1
	p.plans[0][1].items[0] = append(p.plans[0][1].items[0], capacityItem{id: "zero", kappa: .001, e: 2})
	result := p.evaluate([]int{1}, 0)
	if !p.admits(&result) || result.weights[0]["zero"] != 0 || result.sums[0][0] != 20 {
		t.Fatalf("zero weight counted as supply: %+v", result)
	}
	p.plans[0][1].items[0][0].kappa = 19
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) {
		t.Fatal("retirement erased h=1 margin")
	}
	// Current demand always carries the configured reduction headroom.
	p = reductionAdmissionFixture(10, 100, 11)
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) {
		t.Fatal("reduction bypassed headroom")
	}
}

func TestTempoRecoverySurvivesNodeLocalTimingAndNotReadySCH56(t *testing.T) {
	s, snap := schedulerFixture()
	s.ScaleDownHeadroomFraction = .25
	old := snap.Instances[0]
	next := poolsInstance(old, "cuda", old.Node, snap.Nodes[0].PowerMode, "w1")
	profile := *next.Profile
	profile.ID = "cuda"
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{1}
	profile.RuntimeMetadata = &metadata
	next.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(next))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, old.Node, "unloaded", schedulerState(next), snap.Nodes[0].PowerMode),
		poolsCost(schedulerActionRemove, old.Node, schedulerState(old), "unloaded", snap.Nodes[0].PowerMode),
	}
	lost := poolsAddNode(&snap, "rasp5", map[string]float64{"fixed": 10})
	for k := range snap.Nodes {
		if snap.Nodes[k].Node == lost {
			snap.Nodes[k].Ready = false
		}
	}
	capacityLoad(s, &snap, snap.Workloads[0], 72)
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{old.Profile.ID: 64.28502, "cuda": 100}))
	p := capacityPlannerForTest(t, s, snap, rows)
	for n := range p.plans {
		p.plans[n] = p.nodePlans(n, p.keep.Nodes)
	}
	found := false
	for n, plans := range p.plans {
		for j, plan := range plans {
			if !plan.record.Eligible || len(plan.instances) != 1 || plan.instances[0].Profile.ID != "cuda" {
				continue
			}
			choice := make([]int, len(p.plans))
			choice[n] = j
			result := p.evaluate(choice, plan.preMax)
			if p.admits(&result) && result.feasible {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("safe CUDA replacement failed after rasp5 NotReady")
	}
}

func TestTempoMixedUIDCannotLaunderSmolDownscaleSCH56(t *testing.T) {
	p := reductionAdmissionFixture(75, 64, 100)
	smol := capacityWorkload{d: &Definition{UID: "smol"}, rate: .3, slack: 1}
	p.ws = append(p.ws, smol)
	smolOld := SchedulerInstance{ID: "smol-old", WorkloadUID: "smol", Ready: true, Routable: map[string]bool{"smol": true}}
	p.snapshot.Instances = append(p.snapshot.Instances, smolOld)
	p.currentCapacity[currentCapacityKey(smolOld)] = capacityItem{kappa: .75}
	p.keep.Routing["smol"] = map[string]int32{smolOld.ID: 1000}
	for j, plan := range p.plans[0] {
		id, kappa := "smol-old", .75
		if j == 1 {
			id, kappa = "smol-next", .4
		}
		plan.instances = append(plan.instances, SchedulerInstance{ID: id, WorkloadUID: "smol"})
		plan.items = append(plan.items, []capacityItem{{id: id, kappa: kappa, e: 1}})
		plan.retained = append(plan.retained, nil)
		if j == 0 {
			plan.retained[1] = []capacityItem{{id: id, kappa: kappa, weight: 1000}}
		}
	}
	result := p.evaluate([]int{1}, 0)
	if !p.admits(&result) || !result.feasible {
		t.Fatalf("safe mixed-workload recovery was excluded: %+v", result)
	}
	p.plans[0][1].items[1][0].kappa = .372262
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) || result.exclusion != schedulerReductionCapacityInsufficient {
		t.Fatalf("MNV recovery laundered Smol headroom: %+v", result)
	}
}

func TestTempoHeadroomIntegerRoundingSCH55(t *testing.T) {
	p := reductionAdmissionFixture(20, 100, 12.49)
	p.plans[0][1].items[0] = append(p.plans[0][1].items[0], capacityItem{id: "other", kappa: 12.51, e: 1})
	result := p.evaluate([]int{1}, 0)
	if p.admits(&result) {
		t.Fatal("aggregate κ met margin but no integer route carried the protected load")
	}
}
