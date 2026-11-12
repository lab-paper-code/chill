package serving

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestIdleSingleInstanceUsesItsProfileBase(t *testing.T) {
	s, snap := schedulerFixture()
	snap.Nodes[0].PowerMode = "GPU_1500"
	snap.Baselines[0].Watts, snap.Baselines[0].PowerMode = 53.14, "fixed"
	snap.Instances[0].Profile.Condition.PowerMode = "GPU_1500"
	snap.Instances[0].Profile.IdleBaselineWatts = 118.44
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = 8.77
	s.MinimumDemand = map[string]float64{"w1": 1}
	p := capacityPlannerForTest(t, s, snap)
	got := p.evaluateEnergyForRoutes([]int{0}, 0, nil, 1, p.keep.Routing)
	if math.Abs(got.joules-(118.44+8.77)) > 1e-8 {
		t.Fatalf("serving W=%g, want profile base + request increment=%g", got.joules, 118.44+8.77)
	}
	d := p.decide(SchedulerDecision{Keep: p.keep, Settings: s}, p.evaluateCurrentKeep(p.periodSeconds))
	if idle := p.plans[0][0].record.IdleCosts["before"]; idle.RequestWatts != nil || idle.PowerMode != "GPU_1500" || len(idle.State) == 0 {
		t.Fatalf("decision projection changed planner metadata: %+v", idle)
	}
	for _, plan := range d.Capacity.Plans {
		if plan.Key == "keep" && plan.Node.UID == snap.Nodes[0].Node.UID {
			if watts := plan.IdleCosts["before"].RequestWatts; watts == nil || math.Abs(*watts-8.77) > 1e-8 {
				t.Fatalf("request W absent from decision: %+v", plan.IdleCosts)
			}
		}
	}
}

func TestCapacityOtherModeIdleCannotDecideEnergyMove(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = 600 * time.Second
	snap.Nodes[0].PowerMode = "eco"
	snap.Instances[0].Profile.Condition.PowerMode = "eco"
	snap.Instances[0].IdleScope = "unknown"
	snap.Baselines[0].Watts = 1000
	n := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
	snap.Nodes[len(snap.Nodes)-1].PowerMode = "fixed"
	wanted := poolsInstance(snap.Instances[0], "new", n, "fixed", "w1")
	wanted.IdleScope = schedulerNodeWallScope
	wanted.Profile.IdleBaselineWatts = 1
	wanted.Profile.Batches = append([]BatchProfile(nil), wanted.Profile.Batches...)
	wanted.Profile.Batches[0].EnergyPerInputJoules = .01
	snap.Options = append(snap.Options, poolsOption(wanted))
	snap.Costs = append(snap.Costs, poolsCost("prepare", n, "unloaded", schedulerState(wanted), "fixed"), poolsCost("remove", snap.Instances[0].Node, schedulerState(snap.Instances[0]), "unloaded", "eco"))
	capacityLoad(s, &snap, snap.Workloads[0], 10)
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, snap.Nodes[0].Node) {
		t.Fatalf("other-mode baseline authorized retirement of the unknown node state: %+v", d.Target)
	}
	raw, _ := json.Marshal(d.Capacity)
	if !strings.Contains(string(raw), "undecidable") || !strings.Contains(string(raw), "missingCells") {
		t.Fatalf("missing energy measurement absent from record: %s", raw)
	}
	if !slices.ContainsFunc(d.Capacity.UndecidableCandidates, func(candidate SchedulerUndecidableCandidateRecord) bool {
		return candidate.Excluded && slices.ContainsFunc(candidate.MissingCellRefs, func(ref int) bool {
			cell := d.Capacity.MissingCells[ref]
			return cell.Node == snap.Nodes[0].Node && cell.Kind == "" && cell.PowerMode == "eco"
		})
	}) {
		t.Fatalf("other-mode idle target cell not recorded: %+v", d.Capacity.UndecidableCandidates)
	}
}

func TestCapacityTransferredPrepareOnlyBlocksEnergyMove(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "energy", true: "recovery"}[recovery], func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ComparisonPeriod = 600 * time.Second
			snap.Instances[0].Profile.IdleBaselineWatts = 1000
			n := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
			wanted := poolsInstance(snap.Instances[0], "new", n, "fixed", "w1")
			wanted.Profile.IdleBaselineWatts = 1
			snap.Options = append(snap.Options, poolsOption(wanted))
			transferred := poolsCost("prepare", n, "unloaded", "different-profile", "fixed")
			snap.Costs = append(snap.Costs, transferred, poolsCost("remove", snap.Instances[0].Node, schedulerState(snap.Instances[0]), "unloaded", "fixed"))
			rate := 10.0
			if recovery {
				rate = 150
			}
			capacityLoad(s, &snap, snap.Workloads[0], rate)
			d := capacityDecideForTest(t, s, snap)
			if d.Target == nil {
				t.Fatalf("no target: %+v", d)
			}
			if recovery == d.Target.Keep {
				t.Fatalf("recovery=%t target=%+v", recovery, d.Target)
			}
			raw, _ := json.Marshal(d.Capacity)
			if !strings.Contains(string(raw), "undecidable") || !strings.Contains(string(raw), "prepare") {
				t.Fatalf("transferred prepare not disclosed: %s", raw)
			}
		})
	}
}

func TestCapacityIdenticalUnknownIdleCancelsForRouteChange(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = 600 * time.Second
	old := snap.Instances[0]
	old.Profile.Condition.PowerMode = "eco"
	old.IdleScope = "unknown"
	snap.Instances[0] = old
	snap.Nodes[0].PowerMode = "eco"
	n := poolsAddNode(&snap, "ready-cheap", map[string]float64{"fixed": 10})
	snap.Nodes[len(snap.Nodes)-1].PowerMode = "fixed"
	cheap := poolsInstance(old, "ready-cheap", n, "fixed", "w1")
	cheap.Profile.Batches = append([]BatchProfile(nil), cheap.Profile.Batches...)
	cheap.Profile.Batches[0].EnergyPerInputJoules = .01
	cheap.IdleScope = schedulerNodeWallScope
	snap.Instances = append(snap.Instances, cheap)
	snap.Options = append(snap.Options, poolsOption(cheap))
	snap.Routing = map[string]map[string]int32{"w1": {old.ID: schedulerRouteUnits}}
	capacityLoad(s, &snap, snap.Workloads[0], 10)
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || d.Target.Keep || d.Target.Routing["w1"][cheap.ID] <= 0 {
		t.Fatalf("identical unknown blocked route saving: %+v reasons=%v", d.Target, d.Reasons)
	}
	if d.Capacity.EnergyComparison.Status != "decidable" || len(d.Capacity.EnergyComparison.MissingCellRefs) > 0 {
		t.Fatalf("identical terms did not cancel: %+v", d.Capacity.EnergyComparison)
	}
	for _, plan := range d.Capacity.Plans {
		if plan.Node == n && plan.Key == "keep" {
			watts := plan.IdleCosts["after"].RequestWatts
			if watts == nil || *watts <= 0 {
				t.Fatal("decidable route change omitted per-node request watts")
			}
		}
	}
}

func TestIdleExactCellRequiresMatchingMeter(t *testing.T) {
	s, snap := schedulerFixture()
	s.MinimumDemand = map[string]float64{"w1": 1}
	snap.Baselines[0].State = []string{schedulerState(snap.Instances[0])}
	snap.Baselines[0].Meter = "another-node-wall-meter"
	snap.Baselines[0].Watts = 53.14
	snap.Instances[0].Profile.IdleBaselineWatts = 118.44
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = 8.77
	p := capacityPlannerForTest(t, s, snap)
	got := p.evaluateEnergyForRoutes([]int{0}, 0, nil, 1, p.keep.Routing)
	if math.Abs(got.joules-(118.44+8.77)) > 1e-8 {
		t.Fatalf("different-meter idle entered serving prediction: %g", got.joules)
	}
}

func TestCapacityBridgeRecordsPhaseRequestPower(t *testing.T) {
	settings, snapshot, _, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, settings, snapshot)
	rate := 0.0
	for _, workload := range decision.Capacity.Workloads {
		if workload.UID == "w2" {
			rate = workload.Rate
		}
	}
	for _, plan := range decision.Capacity.Plans {
		if plan.Node != bridge.Node || plan.Key == "keep" || plan.IdleCosts["mid"].RequestWatts == nil {
			continue
		}
		mid := plan.IdleCosts["mid"]
		if math.Abs(*mid.RequestWatts-rate) > 1e-8 || !slices.Contains(mid.RequestReferences, bridge.Profile.ID) {
			t.Fatalf("bridge phase lost routed request power: %+v, demand=%g", mid, rate)
		}
		if before := plan.IdleCosts["before"]; before.RequestWatts == nil || *before.RequestWatts != 0 {
			t.Fatalf("unloaded bridge node received pre-prepare request power: %+v", before)
		}
		return
	}
	t.Fatal("selected bridge node omitted phase request power")
}

// fullReplayCapacityExact retains the old replay-first behavior as an oracle.
type fullReplayCapacityExact struct{ *capacityExact }

func (e fullReplayCapacityExact) Evaluate(choice []int) capacityResult {
	return e.p.evaluate(choice, e.p.routeOf(choice))
}

func TestCapacityEnergyUndecidableSkipsReplayWithoutChangingSelection(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, postfilter := range []bool{false, true} {
			t.Run(fmt.Sprintf("recovery=%t/postfilter=%t", recovery, postfilter), func(t *testing.T) {
				var decisions []SchedulerDecision
				var counts []int
				for _, skip := range []bool{false, true} {
					s, snap := schedulerFixture()
					s.PlanSearchStrategy, s.PlanPostFilterFinalists = protocol.V2, postfilter
					s.PlanSearchEvaluations, s.PlanExactFinalists = 1000, 1
					s.ComparisonPeriod = 600 * time.Second
					snap.Instances[0].Profile.IdleBaselineWatts = 1000
					node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
					wanted := poolsInstance(snap.Instances[0], "new", node, "fixed", "w1")
					wanted.Profile.IdleBaselineWatts = 1
					snap.Options = append(snap.Options, poolsOption(wanted))
					snap.Costs = append(snap.Costs,
						poolsCost("prepare", node, "unloaded", "different-profile", "fixed"),
						poolsCost("remove", snap.Instances[0].Node, schedulerState(snap.Instances[0]), "unloaded", "fixed"))
					rate := 10.0
					if recovery {
						rate = 150
					}
					capacityLoad(s, &snap, snap.Workloads[0], rate)
					p := capacityPlannerForTest(t, s, snap)
					e := newCapacityExact(p)
					var exact protocol.Exact[capacityResult] = fullReplayCapacityExact{e}
					if skip {
						exact = e
					}
					outcome, err := (v2.Engine[capacityResult]{}).Search(p.scalarProblem(p.planTable(), time.Time{}), exact)
					if err != nil {
						t.Fatal(err)
					}
					if !outcome.Found || outcome.Diagnostics.Fallback != "" {
						t.Fatalf("fixture did not select through the seeded funnel: %+v", outcome)
					}
					if p.currentKeep().feasible == recovery {
						t.Fatal("fixture keep feasibility does not match recovery case")
					}
					e.record(outcome.Diagnostics)
					best := outcome.Value
					if !postfilter {
						best = p.minimumGain(p.payback(best))
					}
					if err := p.testDecisionRates(best); err != nil {
						t.Fatal(err)
					}
					decision := SchedulerDecision{
						At: p.snapshot.At, Settings: s, Keep: p.keep,
						Search:     schedulerSearchPopulation(p.ctx, s, p.snapshot),
						Workloads:  schedulerWorkloadStates(s, p.snapshot),
						References: slices.Clone(p.snapshot.References),
					}
					decision.Reasons = appendUnique(decision.Reasons, schedulerReplayInitialScope(p.snapshot).evidence...)
					fixSchedulerWorkloads(&decision, p.snapshot)
					decision = p.decide(decision, best)
					markFixedEstimates(&decision)
					search := decision.Capacity.Search
					search.StartedAt, search.FinishedAt = time.Time{}, time.Time{}
					search.Duration, search.SurrogateElapsed, search.ExactElapsed = 0, 0, 0
					for i := range search.Steps {
						search.Steps[i].Duration = 0
					}
					decisions = append(decisions, decision)
					count := 0
					p.feedbackCache.Range(func(_, _ any) bool { count++; return true })
					counts = append(counts, count)
				}
				before, after := decisions[0], decisions[1]
				marked := 0
				for i := range after.Capacity.UndecidableCandidates {
					candidate := &after.Capacity.UndecidableCandidates[i]
					if candidate.ReplaySkipped {
						if !candidate.Excluded {
							t.Fatal("skipped candidate was not marked EnergyUndecidable")
						}
						marked++
						candidate.ReplaySkipped = false
					}
				}
				if marked == 0 && !recovery || marked != 0 && recovery {
					t.Fatalf("recovery=%t skipped markers=%d", recovery, marked)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("preflight changed full decision beyond timing and replaySkipped: before=%+v after=%+v", before, after)
				}
				want := counts[0] - marked
				if recovery {
					want = counts[0]
				}
				if counts[1] != want {
					t.Fatalf("recovery=%t replays before=%d after=%d want=%d", recovery, counts[0], counts[1], want)
				}
			})
		}
	}
}
