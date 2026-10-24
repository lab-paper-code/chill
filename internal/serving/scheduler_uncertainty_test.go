package serving

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// The real immutable-cost -> node chain -> minimumGain flow must not turn B
// into nominal energy or pretend its deterministic error is a statistical CI.
func TestCapacityOperationUncertaintyChangesMinimumGain(t *testing.T) {
	var nominal float64
	for _, bound := range []float64{0, 1e6} {
		s, snap, rows, node, _ := capacityR5Fixture()
		snap.Instances[1].Profile.IdleBaselineWatts = 60 // removing saves about 10 W
		for i := range snap.Costs {
			snap.Costs[i].Trials[0].EnergyUncertaintyJoules = bound
			// A mean of two equally uncertain trials still has bound B, not B/sqrt(2).
			snap.Costs[i].Trials = append(snap.Costs[i].Trials, snap.Costs[i].Trials[0])
		}
		p := capacityPlannerForTest(t, s, snap, rows)
		choice := make([]int, len(p.plans))
		n := slices.IndexFunc(p.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
		choice[n] = slices.IndexFunc(p.plans[n], func(plan *capacityPlan) bool {
			return !plan.keep && plan.record.Eligible && len(plan.instances) == 0
		})
		if choice[n] < 0 {
			t.Fatal("missing measured removal plan")
		}
		best := p.evaluate(choice, p.routeOf(choice))
		if !best.feasible || best.keep {
			t.Fatalf("fixture must have a feasible changed candidate: %+v", best)
		}
		if bound == 0 {
			nominal = best.joules
		} else if best.joules != nominal {
			t.Fatalf("B changed nominal energy: %g != %g", best.joules, nominal)
		}
		if got := p.plans[n][choice[n]].record.EnergyUncertaintyJoules; got != bound {
			t.Fatalf("chain lost immutable trial B: %g != %g", got, bound)
		}
		got := p.minimumGain(best)
		if got.keep != (bound > 0) || got.withinUncertainty != (bound > 0) || got.gainUncertainty != bound {
			t.Fatalf("same nominal, B=%g: keep=%v threshold=%g", bound, got.keep, got.gainUncertainty)
		}
	}
}

func TestCapacityDeterministicBoundsComposeAndCancelSharedEvidence(t *testing.T) {
	source := schedulerBoundSource{reference: evidencestore.ObjectIdentity{Store: "raw", Key: "operation", Size: 1}, trial: 0}
	plan := &capacityPlan{operationBounds: schedulerEnergyBounds{source: 3}}
	bounds := schedulerEnergyBounds{}
	// Two actions reusing one measured trial have 6 J, not sqrt(2)*3 J.
	plan.addBounds(bounds, 0, 60, 1)
	plan.addBounds(bounds, 0, 60, 1)
	if got := bounds.halfWidth(); got != 6 {
		t.Fatalf("repeated evidence was treated as independent: %g", got)
	}
	plan.addBounds(bounds, 0, 60, -1)
	if got := bounds.halfWidth(); got != 3 {
		t.Fatalf("shared evidence did not cancel: %g", got)
	}
	plan.addBounds(bounds, 0, 60, -1)
	if got := bounds.halfWidth(); got != 0 {
		t.Fatalf("identical candidates retained uncertainty: %g", got)
	}
	distinct := source
	distinct.reference.Key = "other-operation"
	bounds[source], bounds[distinct] = 3, -4
	if got := bounds.halfWidth(); got != 7 {
		t.Fatalf("distinct deterministic errors must sum absolute bounds: %g", got)
	}
}

func TestCapacityBaselineUncertaintyPreservesSignedReconstruction(t *testing.T) {
	_, snap := schedulerFixture()
	snap.Baselines[0].WattsUncertainty = 2
	snap.Instances[0].Profile.IdleBaselineWatts = 8 // negative loaded-minus-unloaded delta
	instances := []SchedulerInstance{}
	for count := 0; count <= 2; count++ {
		watts, _, sources, _, reason := schedulerBaseline(snap.Baselines, snap.Nodes[0], instances, snap.Options)
		bound := schedulerBaselineUncertainty(snap.Baselines, sources, count)
		if reason != "" || watts != 10-2*float64(count) || bound.watts != 2*(1-float64(count)) {
			t.Fatalf("%d instances: nominal %g bound %+v reason %s", count, watts, bound, reason)
		}
		instances = append(instances, snap.Instances[0])
	}
	loaded := snap.Baselines[0]
	loaded.ID, loaded.State, loaded.Watts, loaded.WattsUncertainty = "loaded", []string{schedulerState(snap.Instances[0])}, 8, 3
	loaded.Reference.Key = "loaded-idle"
	snap.Baselines = append(snap.Baselines, loaded)
	_, _, sources, _, reason := schedulerBaseline(snap.Baselines, snap.Nodes[0], snap.Instances, snap.Options)
	if got := schedulerBaselineUncertainty(snap.Baselines, sources, 1); reason != "" || got.watts != 3 || got.reference != loaded.Reference {
		t.Fatalf("exact loaded-state evidence must replace, not add unloaded uncertainty: %+v %s", got, reason)
	}
	base := 10.0
	cost := poolsCost(schedulerActionPrepare, snap.Nodes[0].Node, "unloaded", "loaded", "fixed")
	cost.EnergyScope, cost.SubtractedWatts = schedulerTargetBaselineDelta, &base
	cost.Trials = []OperationTrial{{Duration: time.Second, Joules: -4, EnergyUncertaintyJoules: 2}}
	_, _, joules, ok := capacityOperationCost(&cost)
	state := schedulerReplayState{rng: rand.New(rand.NewPCG(1, 0))}
	action := schedulerReplayAction{}
	if !ok || joules != 6 || !state.sampleOperation(&action, &cost) || math.Abs(action.wallRate*1e9-6) > 1e-12 || action.trial.EnergyUncertaintyJoules != 2 {
		t.Fatalf("signed wall reconstruction/B incoherent: %g %+v", joules, action)
	}
}
