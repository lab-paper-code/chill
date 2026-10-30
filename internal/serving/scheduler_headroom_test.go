package serving

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

// Raw decision inputs: control-222-full/obs/20261030T12/scheduler.jsonl.gz
// S419, S607, S714 and S6056. These are admission counterfactuals, not
// execution replays: catalog/bridge effects are tested by the existing DAG suite.
func tempoAdmissionFixture(rate, maximum, supply, target float64) *capacityPlanner {
	uid := "w"
	old := SchedulerInstance{ID: "old", WorkloadUID: uid}
	next := SchedulerInstance{ID: "next", WorkloadUID: uid}
	p := &capacityPlanner{ctx: context.Background(), period: 600,
		settings: SchedulerSettings{Controls: SchedulerControls{Routing: true}, ScaleDownHeadroomFraction: .25},
		snapshot: SchedulerSnapshot{Nodes: []SchedulerNode{{}}, Instances: []SchedulerInstance{old}, ScaleDown: map[string]SchedulerDownState{uid: {WindowMax: maximum, Supply: supply, Allowed: rate < supply, WindowNanos: 300 * time.Second}}},
		ws:       []capacityWorkload{{d: &Definition{UID: uid}, rate: rate, slack: 1}}, compared: []bool{false},
		keep: SchedulerCandidate{Routing: map[string]map[string]int32{uid: {"old": 1000}}},
	}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{old}, items: [][]capacityItem{{{id: "old", kappa: supply, e: 1}}}, retained: [][]capacityItem{{{id: "old", kappa: supply, weight: 1000}}}}
	nextPlan := &capacityPlan{record: SchedulerNodePlan{Tempo: schedulerTempoSlow}, instances: []SchedulerInstance{next}, items: [][]capacityItem{{{id: "next", kappa: target, e: 1}}}, retained: [][]capacityItem{nil}}
	p.plans = [][]*capacityPlan{{keep, nextPlan}}
	return p
}

func TestTempoE53E55DownscaleHeadroomSCH55(t *testing.T) {
	for _, row := range []struct {
		name                          string
		rate, maximum, supply, target float64
	}{
		{"E5-3-S419", 54.333333333333336, 57.31666666666667, 99.64337119287221, 64.28501986002459},
		{"E5-5-S6056", 31.400000000000002, 35.21666666666667, 91.90714633483302, 42.47339792169754},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := tempoAdmissionFixture(row.rate, row.maximum, row.supply, row.target)
			p.ws[0].queued = 2
			if row.name == "E5-3-S419" {
				p.ws[0].queued = 4
			}
			result := p.evaluate([]int{1}, 0)
			if p.admits(&result) || result.exclusion != schedulerWindowCapacityInsufficient {
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
		name          string
		rate, maximum float64
		growing       bool
	}{
		{"S607-growing", 71.86666666666666, 71.86666666666666, true}, {"S714-plateau", 75.2, 78.48333333333333, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := tempoAdmissionFixture(row.rate, row.maximum, 64.285020, 100)
			p.ws[0].growing = row.growing
			p.ws[0].queued = 43
			if row.growing {
				p.ws[0].queued = 22
			}
			result := p.evaluate([]int{1}, 0)
			if !p.admits(&result) || !result.feasible {
				t.Fatalf("proven recovery timing-blocked: %+v", result)
			}
			if p.snapshot.ScaleDown["w"].Allowed {
				t.Fatal("recovery mutated Allowed")
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

func TestTempoHeadroomPositiveWeightsAndIdleSCH55(t *testing.T) {
	p := tempoAdmissionFixture(10, 10, 100, 20)
	p.settings.ScaleDownHeadroomFraction = 1
	p.plans[0][1].items[0] = append(p.plans[0][1].items[0], capacityItem{id: "zero", kappa: 1000, e: 2})
	result := p.evaluate([]int{1}, 0)
	if !p.admits(&result) || result.weights[0]["zero"] != 0 || result.sums[0][0] != 20 {
		t.Fatalf("zero weight counted as supply: %+v", result)
	}
	if !p.idleWindowAllowed(result, map[string]bool{"old": true, "zero": true}) {
		t.Fatal("idle removal lost no positive supply")
	}
	p.plans[0][1].items[0][0].kappa = 19
	if p.idleWindowAllowed(result, map[string]bool{"old": true, "zero": true}) {
		t.Fatal("idle cleanup erased h=1 margin")
	}
	// W=0 still protects the current rate; no historical maximum is required.
	p = tempoAdmissionFixture(10, 0, 100, 11)
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) {
		t.Fatal("W=0 bypassed headroom")
	}
}

func TestTempoRecoverySurvivesNodeLocalTimingAndNotReadySCH56(t *testing.T) {
	s, snap := schedulerFixture()
	s.ScaleDownHeadroomFraction = .25
	old := snap.Instances[0]
	next := poolsInstance(old, "cuda", old.Node, snap.Nodes[0].PowerMode, "w1")
	profile := *next.Profile
	profile.ID = "cuda"
	profile.Condition.CPU.AffinityCPUs = []int{1}
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{1}
	profile.RuntimeMetadata = &metadata
	next.Profile = &profile
	next.Allocation.CPUs = []int{1}
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
	p.ws[0].growing = true
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
		t.Fatal("safe CUDA replacement lost to growth/stabilization timing after rasp5 NotReady")
	}
}

func TestTempoMixedUIDCannotLaunderSmolDownscaleSCH56(t *testing.T) {
	p := tempoAdmissionFixture(75, 78, 64, 100)
	smol := capacityWorkload{d: &Definition{UID: "smol"}, rate: .3, slack: 1}
	p.ws = append(p.ws, smol)
	p.snapshot.ScaleDown["smol"] = SchedulerDownState{Supply: .75, WindowMax: .3}
	p.snapshot.Instances = append(p.snapshot.Instances, SchedulerInstance{ID: "smol-old", WorkloadUID: "smol"})
	for j, plan := range p.plans[0] {
		id, kappa := "smol-old", .75
		if j == 1 {
			id, kappa = "smol-next", .4
		}
		plan.instances = append(plan.instances, SchedulerInstance{ID: id, WorkloadUID: "smol"})
		plan.items = append(plan.items, []capacityItem{{id: id, kappa: kappa, e: 1}})
		plan.retained = append(plan.retained, nil)
	}
	result := p.evaluate([]int{1}, 0)
	if p.admits(&result) || result.exclusion != schedulerScaleDownStabilizing {
		t.Fatalf("MNV recovery laundered Smol downscale: %+v", result)
	}
	p.snapshot.ScaleDown["smol"] = SchedulerDownState{Supply: .75, WindowMax: .3, Allowed: true}
	p.plans[0][1].items[1][0].kappa = .372262
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) || result.exclusion != schedulerWindowCapacityInsufficient {
		t.Fatalf("MNV recovery laundered Smol headroom: %+v", result)
	}
	p.plans[0][1].items[1][0].kappa = .4
	p.ws[1].growing = true
	result = p.evaluate([]int{1}, 0)
	if p.admits(&result) || result.exclusion != schedulerDemandIncreasing {
		t.Fatalf("MNV recovery laundered Smol growth protection: %+v", result)
	}
}

func TestTempoHeadroomIntegerRoundingSCH55(t *testing.T) {
	p := tempoAdmissionFixture(20, 20, 100, 12.49)
	p.plans[0][1].items[0] = append(p.plans[0][1].items[0], capacityItem{id: "other", kappa: 12.51, e: 1})
	result := p.evaluate([]int{1}, 0)
	if p.admits(&result) {
		t.Fatal("aggregate κ met margin but no integer route carried the protected load")
	}
}

func TestTempoHeadroomSettingsWireSCH55(t *testing.T) {
	s, _ := schedulerFixture()
	for _, h := range []float64{0, .25, 1} {
		s.ScaleDownHeadroomFraction = h
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var input SchedulerSettingsInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		got, err := input.Settings()
		if err != nil || got.ScaleDownHeadroomFraction != h {
			t.Fatalf("settings wire lost h=%g: %+v %v", h, got, err)
		}
	}
}
