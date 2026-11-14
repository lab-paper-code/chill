package serving

import (
	"math"
	"testing"
	"time"
)

func schedulerTestAwakeModel(id string, step, hold, slope, saturation, busy float64) *schedulerLoadModel {
	return &schedulerLoadModel{identity: id, profile: id, step: step, hold: hold, stepOK: true, holdOK: true,
		batches: []schedulerLoadBatch{{saturation: saturation, saturationOK: true, slope: slope, slopeOK: true, busy: busy, busyOK: true}}}
}

// Two instances on one node add the awake step once, at the node's input rate
// and utilisation, with S and tau of the configuration with the larger S; on
// two nodes each adds its own.
func TestNodeLoadWattsAddsAwakeStepOncePerNode(t *testing.T) {
	a := schedulerTestAwakeModel("a", 20, 2, 0.5, 100, 0.004)
	b := schedulerTestAwakeModel("b", 30, 0.5, 0.25, 100, 0.002)
	node, other := NodeReference{UID: "n1"}, NodeReference{UID: "n2"}
	together := nodeLoadWatts([]schedulerRate{{id: "a", node: node, model: a, rate: 10}, {id: "b", node: node, model: b, rate: 30}})
	lambda, rho := 40.0, 10*0.004+30*0.002
	want := 30*(1-(1-rho)*math.Exp(-lambda*0.5)) + 0.5*10 + 0.25*30
	if len(together) != 1 || !together[0].known || math.Abs(together[0].watts-want) > 1e-12 {
		t.Fatalf("one node: %+v, want %.12g W", together, want)
	}
	apart := nodeLoadWatts([]schedulerRate{{id: "a", node: node, model: a, rate: 10}, {id: "b", node: other, model: b, rate: 30}})
	wantA := 20*(1-(1-10*0.004)*math.Exp(-10*2.0)) + 5
	wantB := 30*(1-(1-30*0.002)*math.Exp(-30*0.5)) + 7.5
	if len(apart) != 2 || math.Abs(apart[0].watts-wantA) > 1e-12 || math.Abs(apart[1].watts-wantB) > 1e-12 || apart[0].watts+apart[1].watts <= together[0].watts {
		t.Fatalf("two nodes: %+v, want %.12g and %.12g W", apart, wantA, wantB)
	}
}

// A not-identified value the formula needs makes the node's load watts
// unknown, never zero; a zero step needs no hold.
func TestNodeLoadWattsNotIdentifiedIsUnknown(t *testing.T) {
	node := NodeReference{UID: "n1"}
	step := schedulerTestAwakeModel("step", 0, 0, 1, 100, 0.001)
	step.stepOK = false
	hold := schedulerTestAwakeModel("hold", 5, 0, 1, 100, 0.001)
	hold.holdOK = false
	slope := schedulerTestAwakeModel("slope", 5, 1, 0, 100, 0.001)
	slope.batches[0].slopeOK = false
	for _, model := range []*schedulerLoadModel{step, hold, slope} {
		if load := nodeLoadWatts([]schedulerRate{{id: "i", node: node, model: model, rate: 10}}); len(load) != 1 || load[0].known {
			t.Fatalf("%s: %+v", model.identity, load)
		}
	}
	flat := schedulerTestAwakeModel("flat", 0, 0, 1, 100, 0.001)
	flat.holdOK = false
	if load := nodeLoadWatts([]schedulerRate{{id: "i", node: node, model: flat, rate: 10}}); !load[0].known || load[0].watts != 10 {
		t.Fatalf("zero step needs no hold: %+v", load)
	}
}

// The slope is the smallest measured batch's whose saturation rate covers
// the instance's rate, else the largest measured batch's.
func TestLoadModelPicksCoveringBatch(t *testing.T) {
	_, snap := schedulerFixture()
	p := *snap.Instances[0].Profile
	first, second := 50.0, 120.0
	p.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1, InputSlopeJoules: schedulerTestSlope(3), SaturationInputRate: &first}, []float64{20e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 4, InputSlopeJoules: schedulerTestSlope(1), SaturationInputRate: &second}, []float64{40e6}),
	}
	m := newSchedulerLoadModel(&p, 4)
	for rate, want := range map[float64]float64{10: 3, 50: 3, 80: 1, 500: 1} {
		if b := m.batchAt(rate); b.slope != want {
			t.Fatalf("rate %g: slope %g, want %g", rate, b.slope, want)
		}
	}
	if b := newSchedulerLoadModel(&p, 1).batchAt(500); b.slope != 3 {
		t.Fatalf("the batch limit bounds the measured batches: %+v", b)
	}
}

// A profile whose awake step is not identified leaves exact energy without its
// node load: the keep result holds the load as an unknown and reports it as a
// missing cell against a reference without it, the path a missing idle or
// operation measurement takes.
func TestCapacityNotIdentifiedPowerIsMissingCell(t *testing.T) {
	s, snap := schedulerFixture()
	profile := *snap.Instances[0].Profile
	profile.Power = &ProfilePower{IdleWatts: schedulerTestIdentified(12), AwakeStepWatts: schedulerTestNotIdentified("inseparable from the input slope"), HoldSeconds: schedulerTestNotIdentified("no decay")}
	snap.Instances[0].Profile = &profile
	snap.Options[0].Instance.Profile = &profile
	capacityLoad(s, &snap, snap.Workloads[0], 10)
	p := capacityPlannerForTest(t, s, snap)
	keep := p.currentKeep()
	if len(keep.loadUnknowns) == 0 || math.Abs(keep.joules-p.plans[0][0].energy(0, keep.period)) > 1e-9 {
		t.Fatalf("not-identified step became a number: joules %g unknowns %+v", keep.joules, keep.loadUnknowns)
	}
	cells := p.missingEnergyCells(capacityResult{}, keep)
	if len(cells) != 1 || cells[0].Kind != schedulerLoadPowerCell || cells[0].Node != snap.Nodes[0].Node {
		t.Fatalf("missing load cell: %+v", cells)
	}
	if same := p.missingEnergyCells(keep, keep); len(same) != 0 {
		t.Fatalf("the same unknown load cancels: %+v", same)
	}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.energyUnknown) == 0 {
		t.Fatalf("replay energy must be unknown: %+v", result)
	}
}

// The search's per-move node load equals the full recomputation when two
// instances share a node with an awake step (concave, not additive).
func TestCapacityDeltaEqualsRecomputedSharedNodeLoad(t *testing.T) {
	p := capacityDeltaTiny([]float64{30, 40}, 25)
	p.settings.Controls.Routing = false
	node := p.snapshot.Nodes[0].Node
	a := schedulerTestAwakeModel("a", 12, 1.5, 0.3, 30, 0.02)
	b := schedulerTestAwakeModel("b", 18, 0.4, 0.2, 40, 0.01)
	keep := p.plans[0][0]
	for k := range keep.items[0] {
		keep.items[0][k].node, keep.items[0][k].model = node, a
	}
	for k := range keep.retained[0] {
		keep.retained[0][k].node, keep.retained[0][k].model = node, a
	}
	shared := *keep
	shared.keep, shared.record.Key = false, "shared"
	extra := capacityItem{id: "b", routingWeight: 40, throughput: 40, serviceNanos: 10e6, order: 0.2, node: node, model: b, weight: 2}
	shared.items = [][]capacityItem{append([]capacityItem{keep.items[0][0]}, extra)}
	shared.preMax, shared.preMean, shared.mid, shared.after = 2, 2, 10, 10
	p.plans[0] = append(p.plans[0], &shared)
	other := p.snapshot.Nodes[1].Node
	for _, item := range [][]capacityItem{p.plans[1][0].items[0], p.plans[1][0].retained[0]} {
		for k := range item {
			item[k].node, item[k].model = other, schedulerTestAwakeModel("c", 7, 3, 0.1, 40, 0.015)
		}
	}
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
	key, exact := capacityDeltaAssert(t, p, s, 0, tab.byPlan[0][1])
	if key.joules == 0 || len(exact.loadUnknowns) != 0 {
		t.Fatalf("shared-node move: key %+v exact %+v", key, exact)
	}
	back, _ := capacityDeltaAssert(t, p, s, 1, tab.byPlan[1][0])
	if back.joules == key.joules {
		t.Fatalf("a move on the other node kept the moved plan's load: %g", back.joules)
	}
}

// Occupancy source order: serving-measured, then the profile batch's own
// samples (their median), then the dispatch-occupancy measurement, then the
// configured default.
func TestResolveDispatchOccupancyReadsProfileBeforeDOM(t *testing.T) {
	s, snap := schedulerFixture()
	s.DefaultDispatchOccupancyNanos = 7
	i := snap.Instances[0]
	profile := *i.Profile
	profile.Batches = []BatchProfile{profile.Batches[0]}
	profile.Batches[0].DispatchOccupancyNanos = []float64{300, 100, 200}
	i.Profile = &profile
	c := profile.Condition
	cell := DispatchOccupancy{Node: i.Node, PowerMode: c.PowerMode, Model: c.LogicalModel, RuntimeImage: c.RuntimeImage, ProfileID: profile.ID, ExecutionPath: c.Artifact.ExecutionPath, Batch: i.MaxBatch}
	dom, measured := cell, cell
	dom.PerRequest, dom.Provenance = 50, &DispatchOccupancyProvenance{Source: occupancySourceDOM}
	measured.PerRequest, measured.Provenance = 400, &DispatchOccupancyProvenance{Source: occupancySourceMeasured}
	for _, tc := range []struct {
		name    string
		cells   []DispatchOccupancy
		samples []float64
		want    time.Duration
		source  string
	}{
		{"measured", []DispatchOccupancy{dom, measured}, []float64{300, 100, 200}, 400, occupancySourceMeasured},
		{"profile", []DispatchOccupancy{dom}, []float64{300, 100, 200}, 200, occupancySourceProfile},
		{"dom", []DispatchOccupancy{dom}, nil, 50, occupancySourceDOM},
		{"default", nil, nil, 7, occupancySourceDefault},
	} {
		profile.Batches[0].DispatchOccupancyNanos = tc.samples
		got := ResolveDispatchOccupancy(s, tc.cells, i)
		if got.PerRequest != tc.want || got.Provenance.Source != tc.source {
			t.Fatalf("%s: %+v %+v", tc.name, got, got.Provenance)
		}
	}
}

// genesis2 image classification on CUDA, stored session a1 of the derivation's
// fixture (internal/profiling/derivation/testdata/session_trials.json.gz),
// as DeriveSession fits it: idle 66.4 W, step 52.65 W, hold 16.56 s, best-fit
// residual 11.86 W^2 within a noise limit of 38.89 W^2. Each level ran on a
// fixed batch-B instance, so its model holds batch B alone.
func TestNodeLoadWattsReproducesStoredGenesis2Levels(t *testing.T) {
	const idle, step, hold, limit = 66.4, 52.65, 16.56, 38.89200534736913
	type level struct{ rate, watts float64 }
	batches := []struct {
		batch                    int
		saturation, slope, nanos float64
		levels                   []level
	}{
		{1, 132.77031836654859, 0.27363333388084343, 9175530.5681147799, []level{{0.9151373895762972, 119.14611221186006}, {132.77031836654859, 155.70562333790963}, {33.796649625467133, 127.04971171323574}}},
		{2, 141.2613286256815, 0.25914842430608659, 16432416.760616304, []level{{141.2613286256815, 156.13817864188465}, {35.129785225025607, 126.24724475179266}}},
		{4, 147.13314672409405, 0.25869191677006614, 30396468.885672882, []level{{147.13314672409405, 156.76474952061167}, {36.085679787462006, 129.82753022167435}}},
		{8, 144.27494826970675, 0.26927831603028135, 60383120.815138236, []level{{144.27494826970675, 157.39241658830798}, {37.808582812694162, 131.19300471463455}}},
	}
	node := NodeReference{UID: "genesis2"}
	squares := 0.0
	for _, b := range batches {
		profile := &Profile{ID: "genesis2", Power: schedulerTestAwakePower(idle, step, hold),
			Batches: []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: b.batch, InputSlopeJoules: schedulerTestSlope(b.slope), SaturationInputRate: &b.saturation}, []float64{b.nanos})}}
		model := newSchedulerLoadModel(profile, b.batch)
		for _, l := range b.levels {
			load := nodeLoadWatts([]schedulerRate{{id: "i", node: node, model: model, rate: l.rate}})
			predicted := idle + load[0].watts
			squares += (predicted - l.watts) * (predicted - l.watts)
			// The derivation's acceptance bound for genesis2 levels is 3.2 W.
			if !load[0].known || math.Abs(predicted-l.watts) > 3.2 {
				t.Fatalf("b%d at %g/s: predicted %g W, stored %g W", b.batch, l.rate, predicted, l.watts)
			}
		}
	}
	if squares > limit {
		t.Fatalf("squared error %g W^2 exceeds the fit's noise limit %g W^2", squares, limit)
	}
}
