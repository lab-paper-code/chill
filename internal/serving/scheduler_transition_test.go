package serving

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const preparedPhysicalID = "prepared-physical"

// preparedSlot is the n2 option already prepared as a ready, unrouted physical
// instance whose slot is the planner's target ID for it.
func preparedSlot(snapshot SchedulerSnapshot, n2 NodeReference) SchedulerInstance {
	i := slices.IndexFunc(snapshot.Options, func(o SchedulerOption) bool {
		return o.Instance.Node == n2 && o.Instance.Allocation.GPUUUID == ""
	})
	prepared := snapshot.Options[i].Instance
	prepared.TargetID = fmt.Sprintf("target-%s-%s-%d", prepared.WorkloadUID, prepared.Profile.ID, prepared.MaxBatch)
	prepared.ID, prepared.WorkloadUID, prepared.Ready = preparedPhysicalID, "", true
	return prepared
}

// moveFixture moves w1 from i1 on n1 to the prepared slot on n2.
func moveFixture(t *testing.T) (SchedulerSnapshot, SchedulerCandidate) {
	t.Helper()
	_, snapshot, n2 := placementFixture()
	prepared := preparedSlot(snapshot, n2)
	snapshot.Instances = append(snapshot.Instances, prepared)
	snapshot.Routing = map[string]map[string]int32{"w1": {"i1": 1}}
	wanted := prepared
	wanted.WorkloadUID = "w1"
	target := SchedulerCandidate{ID: "move", Nodes: snapshot.Nodes, Instances: []SchedulerInstance{wanted}, Routing: map[string]map[string]int32{"w1": {prepared.ID: 1}}}
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	return snapshot, target
}

func requireTransitionAction(t *testing.T, actions []Action, kind string) Action {
	t.Helper()
	index := slices.IndexFunc(actions, func(action Action) bool { return action.Kind == kind })
	if index < 0 {
		t.Fatalf("missing %s action: %+v", kind, actions)
	}
	return actions[index]
}

func TestObservedPowerRebootPlansRemovalBeforePower(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("known=%v", known), func(t *testing.T) {
			settings, snapshot, target := overlappingResourceFixture(t)
			node := &snapshot.Nodes[0]
			from := node.PowerMode
			if known {
				node.Reboots = []PowerReboot{{From: from, To: "next"}}
			} else {
				node.Reboots = []PowerReboot{{From: "next", To: from}}
			}
			target.Nodes = slices.Clone(snapshot.Nodes)
			target.Nodes[0].PowerMode = "next"
			target.Actions = schedulerPlan(settings, snapshot, target)
			remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
			power := requireTransitionAction(t, target.Actions, schedulerActionPower)
			prepare := requireTransitionAction(t, target.Actions, schedulerActionPrepare)
			if known {
				if !slices.Contains(power.DependsOn, remove.ID) || slices.Contains(remove.DependsOn, power.ID) || !slices.Contains(prepare.DependsOn, power.ID) {
					t.Fatalf("known reboot did not remove/power/rebuild: %+v", target.Actions)
				}
			} else if !slices.Contains(remove.DependsOn, power.ID) {
				t.Fatalf("unknown transition changed in-place ordering: %+v", target.Actions)
			}
			if result := schedulerReplayForTest(settings, snapshot, target); len(result.reasons) != 0 {
				t.Fatalf("power lifecycle replay failed: %+v", result.reasons)
			}
		})
	}
}

// Selecting the same measured physical condition reuses its ready prepared slot.
// Another eligible profile may still win the full optimizer's energy comparison.
func TestPlacementReusesReadyUnboundPhysicalSlotWithoutDuplicatePreparation(t *testing.T) {
	settings, snapshot, n2 := placementFixture()
	prepared := preparedSlot(snapshot, n2)
	snapshot.Instances = append(snapshot.Instances, prepared)
	capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
	planner := capacityPlannerForTest(t, settings, snapshot)
	choice := capacityChoiceForTest(t, planner, map[string]string{n2.UID: prepared.Profile.ID + "/b1/w1"})
	reuse := planner.evaluate(choice, 0)
	d := planner.decide(SchedulerDecision{Keep: planner.keep}, reuse)
	optimized := capacityDecideForTest(t, settings, snapshot)
	if optimized.Target == nil {
		t.Fatal("no optimizer target")
	}
	t.Logf("same-condition reuse J=%g; optimizer J=%g instances=%v", reuse.joules, *optimized.Target.ExpectedJoules, schedulerConfigKey(*optimized.Target))
	if !reuse.feasible || d.Target == nil || !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == preparedPhysicalID && i.WorkloadUID == "w1" }) {
		t.Fatalf("matching actual ready slot was not reused: %+v", d.Target)
	}
	if slices.ContainsFunc(d.Target.Actions, func(action Action) bool {
		return action.Kind == schedulerActionPrepare || action.Instance == preparedPhysicalID && action.Kind == schedulerActionRemove
	}) {
		t.Fatalf("already prepared destination was prepared twice or removed: %+v", d.Target.Actions)
	}
	if result := schedulerReplayForTest(settings, snapshot, *d.Target); len(result.reasons) != 0 {
		t.Fatalf("ready physical reuse introduced a transition failure: %v", result.reasons)
	}
	if result := schedulerReplayForTest(settings, snapshot, *optimized.Target); len(result.reasons) != 0 {
		t.Fatalf("optimizer's chosen physical condition introduced a transition failure: %v", result.reasons)
	}
}

func TestCompletedSemanticRouteSurvivesWitnessAndOrdinalRefresh(t *testing.T) {
	snapshot, target := moveFixture(t)
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	route.Status = schedulerActionComplete
	snapshot.Actions = []Action{route}
	snapshot.Routing = target.Routing
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.ID = "another-plan-id"
	target.Instances[0].ObservedAt = snapshot.At
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	reused := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	if reused.ID != route.ID || reused.Status != schedulerActionComplete {
		t.Fatalf("rolling witness/ID restarted the already applied semantic route: old=%+v new=%+v", route, reused)
	}
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	if len(stop.DependsOn) != 0 {
		t.Fatalf("already unrouted source still waits for the refreshed route: %+v", stop)
	}
	stop.Status = schedulerActionComplete
	snapshot.Actions = append(snapshot.Actions, stop)
	snapshot.Instances[0].Stopped = true
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	if actual := requireTransitionAction(t, target.Actions, schedulerActionStop); actual.ID != stop.ID || actual.Status != schedulerActionComplete {
		t.Fatalf("completed stop was restarted before drain: %+v", actual)
	}
	if drain := requireTransitionAction(t, target.Actions, schedulerActionDrain); !slices.Contains(drain.DependsOn, stop.ID) {
		t.Fatalf("drain lost existing completed stop: %+v", drain)
	}
}

func TestConsumedPolicyAndActualTargetChangesStillReplanRoute(t *testing.T) {
	const deadlineChange = "deadline"
	for _, change := range []string{"batch", "wait", "capacity", deadlineChange, "routing", "physical target"} {
		t.Run(change, func(t *testing.T) {
			snapshot, target := moveFixture(t)
			completed := requireTransitionAction(t, target.Actions, schedulerActionRoute)
			completed.Status = schedulerActionComplete
			snapshot.Actions = []Action{completed}
			snapshot.Routing = target.Routing
			snapshot.At = snapshot.At.Add(10 * time.Second)
			switch change {
			case "batch":
				target.Instances[0].MaxBatch = 1
			case "wait":
				target.Instances[0].MaxWait = time.Millisecond
			case "capacity":
				target.Instances[0].Capacity++
			case deadlineChange:
				profile := *target.Instances[0].Profile
				policy := *profile.Condition.Firstfloor
				policy.DeadlinePolicy = "drop"
				profile.Condition.Firstfloor = &policy
				target.Instances[0].Profile = &profile
			case "routing":
				target.Routing = map[string]map[string]int32{"w1": {target.Instances[0].ID: 3}}
			case "physical target":
				target.Instances = []SchedulerInstance{snapshot.Instances[0]}
				target.Routing = map[string]map[string]int32{"w1": {snapshot.Instances[0].ID: 1}}
			}
			target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
			if updated := requireTransitionAction(t, target.Actions, schedulerActionRoute); change == deadlineChange && (updated.ID != completed.ID || updated.Status != schedulerActionComplete) || change != deadlineChange && (updated.ID == completed.ID || updated.Status != schedulerActionPlanned) {
				t.Fatalf("real consumed quantity/target change reused an old completed route: %+v", updated)
			}
		})
	}
}

func TestCompletedActionsRequireTheirCurrentSnapshotEffect(t *testing.T) {
	for _, kind := range []string{schedulerActionPrepare, schedulerActionRoute, schedulerActionStop, schedulerActionDrain, schedulerActionRemove, schedulerActionPower} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%v", kind, present), func(t *testing.T) {
				_, snapshot := schedulerFixture()
				instance := snapshot.Instances[0]
				snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {instance.ID: 1}}
				target := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: append([]SchedulerInstance(nil), snapshot.Instances...), Routing: snapshot.Routing}
				old := Action{ID: "old-terminal-action", Kind: kind, Node: instance.Node, Instance: instance.ID, Status: schedulerActionComplete}
				switch kind {
				case schedulerActionPrepare:
					old.To = schedulerState(instance)
					if !present {
						snapshot.Instances = nil
					}
				case schedulerActionRoute:
					old.Node, old.Instance, old.To = NodeReference{}, "", schedulerConfigKey(target)
					if !present {
						snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {}}
					}
				case schedulerActionStop:
					if present {
						snapshot.Instances[0].Stopped = true
						snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {}}
					}
				case schedulerActionDrain:
					snapshot.Instances[0].Stopped = true
					snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {}}
					if !present {
						snapshot.Outstanding = []SchedulerRequest{{ID: "accepted-native-call", Instance: instance.ID}}
					}
				case schedulerActionRemove:
					if present {
						snapshot.Instances = nil
						snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {}}
					}
				case schedulerActionPower:
					old.Instance, old.To = "", snapshot.Nodes[0].PowerMode
					if !present {
						old.To += "-unobserved"
					}
				}
				snapshot.Actions = []Action{old}
				snapshot.At = snapshot.At.Add(time.Second)
				plan := schedulerPlanState{snap: snapshot, target: &target, nodeTail: map[string]string{}, stops: map[string]string{}}
				plan.add(old.Kind, old.Node, old.Instance, old.From, old.To, nil)
				actual := plan.actions[0]
				if present && (actual.ID != old.ID || actual.Status != schedulerActionComplete) {
					t.Fatalf("actual effect did not retain its completed action: %+v", actual)
				}
				if !present && (actual.ID == old.ID || actual.Status != schedulerActionPlanned) {
					t.Fatalf("terminal flag hid an absent actual effect: %+v", actual)
				}
			})
		}
	}
}

func TestCompletedRouteCannotAliasAnotherExecutorWithIdenticalMeasuredCondition(t *testing.T) {
	snapshot, first := moveFixture(t)
	route := requireTransitionAction(t, first.Actions, schedulerActionRoute)
	route.Status = schedulerActionComplete
	snapshot.Actions, snapshot.Routing = []Action{route}, first.Routing
	other := first.Instances[0]
	other.ID = "another-physical-executor"
	snapshot.Instances = append(snapshot.Instances, other)
	second := first
	second.Instances = []SchedulerInstance{other}
	second.Routing = map[string]map[string]int32{"w1": {other.ID: 1}}
	snapshot.At = snapshot.At.Add(time.Second)
	second.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, second)
	actual := requireTransitionAction(t, second.Actions, schedulerActionRoute)
	if actual.ID == route.ID || actual.Status != schedulerActionPlanned {
		t.Fatalf("same Node/profile/allocation hid the changed physical destination: %+v", actual)
	}
	index := slices.IndexFunc(second.Actions, func(action Action) bool {
		return action.Kind == schedulerActionStop && action.Instance == first.Instances[0].ID
	})
	if index < 0 {
		t.Fatal("missing retirement of the currently routed executor")
	}
	if stop := second.Actions[index]; !slices.Contains(stop.DependsOn, actual.ID) {
		t.Fatalf("old executor could stop before the changed destination was routed: %+v", stop)
	}
}

// An unrouted ready instance is reused only with its slot or w1's ownership as proof.
func TestUnboundPreparedReuseRetainsActualOwnershipFenceAndSlotProvenance(t *testing.T) {
	for _, owner := range []string{"scheduler-slot", "unproven-ownerless", "different-owner", "same-owner"} {
		t.Run(owner, func(t *testing.T) {
			settings, snapshot, n2 := placementFixture()
			prepared := preparedSlot(snapshot, n2)
			switch owner {
			case "unproven-ownerless":
				prepared.TargetID = ""
			case "different-owner":
				prepared.OwnerWorkloadUID = "another-workload-uid"
			case "same-owner":
				prepared.OwnerWorkloadUID, prepared.TargetID = "w1", ""
			}
			snapshot.Instances = append(snapshot.Instances, prepared)
			capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
			planner := capacityPlannerForTest(t, settings, snapshot)
			reused := false
			for _, plans := range planner.plans {
				for _, plan := range plans {
					if !plan.record.Eligible {
						continue
					}
					for _, i := range plan.instances {
						if i.ID == preparedPhysicalID && i.WorkloadUID == "w1" {
							reused = true
							if i.OwnerWorkloadUID != prepared.OwnerWorkloadUID {
								t.Fatal("reuse discarded the captured Workload ownership fence")
							}
						}
					}
				}
			}
			// The optimizer may choose another safe executor as estimated costs
			// change. Eligibility, not that ranking, proves the ownership rule.
			if want := owner == "scheduler-slot" || owner == "same-owner"; reused != want {
				t.Fatalf("ownership/slot fact %s reusable=%v, want %v", owner, reused, want)
			}
			d := capacityDecideForTest(t, settings, snapshot)
			if d.Target == nil {
				t.Fatal("no decision target")
			}
		})
	}
}

func TestResumedExecutorCannotReuseHistoricalDrainBeforeNewStopAndHandoff(t *testing.T) {
	snapshot, target := moveFixture(t)
	source := snapshot.Instances[0]
	snapshot.Instances[0].Stopped = true
	snapshot.Routing = target.Routing
	historical := schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	for _, kind := range []string{schedulerActionRoute, schedulerActionStop, schedulerActionDrain} {
		action := requireTransitionAction(t, historical, kind)
		action.Status = schedulerActionComplete
		snapshot.Actions = append(snapshot.Actions, action)
	}
	// The old drained executor resumed serving with no outstanding request.
	snapshot.Instances[0].Stopped = false
	snapshot.Routing = map[string]map[string]int32{"w1": {source.ID: 1}}
	snapshot.Outstanding = nil
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
	remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
	for _, action := range []Action{route, stop, drain} {
		if old := requireTransitionAction(t, historical, action.Kind); action.ID == old.ID || action.Status != schedulerActionPlanned {
			t.Fatalf("resumed source reused a historical %s effect: %+v", action.Kind, action)
		}
	}
	if !slices.Contains(stop.DependsOn, route.ID) || !slices.Contains(drain.DependsOn, stop.ID) || !slices.Contains(remove.DependsOn, drain.ID) {
		t.Fatalf("resumed source removal bypassed its fresh handoff/stop/drain chain: %+v", target.Actions)
	}
}

// overlappingResourceFixture replaces i1 within a bounded memory domain,
// requiring physical removal before preparation.
func overlappingResourceFixture(t *testing.T) (SchedulerSettings, SchedulerSnapshot, SchedulerCandidate) {
	t.Helper()
	settings, snapshot, _ := placementFixture()
	snapshot.Nodes = snapshot.Nodes[:1]
	source := snapshot.Instances[0]
	snapshot.Routing = map[string]map[string]int32{"w1": {source.ID: 3}}
	wanted := poolsInstance(source, "wide-target", source.Node, "fixed", "w1")
	profile := *wanted.Profile
	profile.ID = "profile-wide"
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity, metadata.IntraOpThreads = []int{0, 1}, 2
	profile.RuntimeMetadata = &metadata
	wanted.Profile, wanted.IdleMeter = &profile, source.IdleMeter
	snapshot.Nodes[0].MemoryBytes = source.Allocation.MemoryBytes
	snapshot.Options = []SchedulerOption{poolsOption(wanted)}
	snapshot.Costs = []OperationCost{
		{ID: "overlap-remove", Node: source.Node, Kind: schedulerActionRemove, From: schedulerState(source), To: "unloaded",
			PowerMode: "fixed", BeforeState: []string{schedulerState(source)}, Trials: []OperationTrial{{Duration: 28 * time.Millisecond, Joules: 1}},
			EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope,
			Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "overlap-remove", Size: 1}},
		{ID: "overlap-prepare", Node: wanted.Node, Kind: schedulerActionPrepare, From: "unloaded", To: schedulerState(wanted),
			PowerMode: "fixed", Trials: []OperationTrial{{Duration: 9 * time.Millisecond, Joules: 1}},
			EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope,
			Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "overlap-prepare", Size: 1}},
	}
	target := SchedulerCandidate{ID: "wide", Nodes: snapshot.Nodes, Instances: []SchedulerInstance{wanted}, Routing: map[string]map[string]int32{"w1": {wanted.ID: 3}}}
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	return settings, snapshot, target
}

func TestOverlappingResourceChangeDrainsAcceptedIngressBeforeMeasuredBreak(t *testing.T) {
	settings, snapshot, target := overlappingResourceFixture(t)
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
	remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
	prepare := requireTransitionAction(t, target.Actions, schedulerActionPrepare)
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	if slices.Contains(stop.DependsOn, route.ID) || !slices.Contains(drain.DependsOn, stop.ID) ||
		!slices.Contains(remove.DependsOn, drain.ID) || !slices.Contains(prepare.DependsOn, remove.ID) || !slices.Contains(route.DependsOn, prepare.ID) {
		t.Fatalf("exclusive allocation replacement is not stop/drain/remove/prepare/route: %+v", target.Actions)
	}
	d := snapshot.Workloads[0]
	snapshot.Outstanding = []SchedulerRequest{{ID: "accepted", Definition: d, ReceivedAt: snapshot.At, Phase: "pending"}}
	result := schedulerReplayForTest(settings, snapshot, target)
	if len(result.reasons) != 0 || len(result.samples[d.Identity()]) != 1 || result.outcomes[d.Identity()]["onTime"]+result.outcomes[d.Identity()]["late"] != 1 {
		t.Fatalf("accepted request did not complete exactly once: %+v", result)
	}
	for _, cost := range snapshot.Costs {
		if !slices.Contains(result.sources, cost.ID) {
			t.Fatalf("replacement omitted the real removal/preparation trial: %+v", result)
		}
	}
}

func TestOverlappingResourceChangeWaitsForAcceptedResponseBeforeRemoval(t *testing.T) {
	settings, snapshot, target := overlappingResourceFixture(t)
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
	remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
	snapshot.Outstanding = []SchedulerRequest{{ID: "accepted", Definition: snapshot.Workloads[0], ReceivedAt: snapshot.At, Phase: "pending"}}
	state := schedulerReplayState{
		settings: settings, snap: snapshot, c: target, horizon: float64(settings.ComparisonPeriod), rng: rand.New(rand.NewPCG(schedulerTestSeed, 0)),
		out: schedulerReplayResult{samples: map[string][]float64{}, definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}},
	}
	state.initializeInstances()
	state.initializeActions()
	state.initializeOutstanding()
	state.admitArrivals(context.Background())
	state.routeCredits = map[string][]int64{}
	state.weights, state.available = make([]int32, len(state.instances)), make([]bool, len(state.instances))
	if !state.startActions() || state.actionByID[stop.ID].done || state.actionByID[remove.ID].started {
		t.Fatalf("future target bypassed the accepted-ingress stop guard: %+v", state.out)
	}
	state.routeRequests(context.Background())
	for _, request := range state.requests {
		if !request.dispatched || request.source.Instance != snapshot.Instances[0].ID {
			t.Fatalf("accepted input moved to the future instance instead of its actual route: %+v", request)
		}
	}
	state.receiveNative()
	if !state.chooseBatches(context.Background()) || !state.startActions() || state.actionByID[drain.ID].done {
		t.Fatalf("stop did not retain the actual nonpreemptive native work until drain: %+v", state.out)
	}
	source := state.byID[snapshot.Instances[0].ID]
	for range len(snapshot.Outstanding) {
		if source.outstanding == 0 {
			break
		}
		if !schedulerFinite(source.finish) || state.actionByID[remove.ID].started {
			t.Fatal("source removal bypassed accepted native completion")
		}
		state.now = source.finish
		state.completeExecution()
		if !state.startActions() || !state.chooseBatches(context.Background()) {
			t.Fatalf("accepted native queue could not finish nonpreemptively: %+v", state.out)
		}
	}
	removal := state.actionByID[remove.ID]
	if source.outstanding != 0 || !removal.started {
		t.Fatal("measured removal did not start after actual accepted drain")
	}
	for _, request := range state.requests {
		if !request.completed || request.source.Instance != source.config.ID || removal.start < request.completion {
			t.Fatalf("retirement lost original assignment or ran before accepted response completion: %+v", request)
		}
	}
}

func TestLegalOverlappingResourceDAGStillReportsMeasuredOutageOverload(t *testing.T) {
	settings, snapshot, target := overlappingResourceFixture(t)
	settings.ObservationWindow, settings.ComparisonPeriod = 40*time.Second, 40*time.Second
	snapshot.Costs[0].Trials[0] = OperationTrial{Duration: 28 * time.Second, Joules: 560}
	snapshot.Costs[1].Trials[0] = OperationTrial{Duration: 9 * time.Second, Joules: 180}
	d := snapshot.Workloads[0]
	d.Admission.Policy = AdmissionPolicyDelay // expose the outage's late completion, not gateway rejection
	snapshot.Arrivals = []SchedulerArrival{{ID: "arrival", Definition: d, Compute: true, At: snapshot.At.Add(-settings.ObservationWindow + time.Millisecond)}}
	evaluated, err := schedulerReplayEstimate(context.Background(), settings, snapshot, target, schedulerTestSeed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluated.Reasons) != 0 || evaluated.ExpectedJoules == nil || evaluated.Status != SchedulerStatusOverload {
		t.Fatalf("legal break must retain measured outage/SLO evaluation, not force feasibility: %+v", evaluated)
	}
	if e := schedulerRequireEstimate(t, evaluated, d.UID); e.NormalizedExceedance <= 0 || e.ExpectedNanos == nil ||
		*e.ExpectedNanos < float64(snapshot.Costs[0].Trials[0].Duration+snapshot.Costs[1].Trials[0].Duration-time.Millisecond) {
		t.Fatalf("unchanged SLO did not expose the measured 37s outage: %+v", e)
	}
}

// The owner retains its intent across an actual empty interval, but the planner
// never promotes that preparing capacity to current κ or duplicates its DAG.
func TestResourceReservationSurvivesActualZeroBindingInterval(t *testing.T) {
	settings, snapshot, target := overlappingResourceFixture(t)
	reserveTargetForTest(&snapshot, &target)
	snapshot.Instances = nil
	snapshot.Routing = map[string]map[string]int32{"w1": {}}
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Target == nil || !decision.Target.Keep || len(decision.Target.Instances) != 0 ||
		len(snapshot.Reservations[0].Target.Instances) != 1 {
		t.Fatalf("empty physical interval changed the reserved intent: %+v", decision)
	}
	if slices.ContainsFunc(decision.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare }) {
		t.Fatal("planner duplicated the reserved preparation")
	}
	if target.Instances[0].WorkloadUID != "w1" {
		t.Fatal("owner identity changed")
	}
}

func TestRunningRouteFixesOnlyItsOwnerScope(t *testing.T) {
	settings, snapshot, _ := overlappingResourceFixture(t)
	current := SchedulerCandidate{ID: "running-owner", Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing}
	running := Action{ID: "running-route", Kind: schedulerActionRoute, Status: phaseRunning, To: schedulerConfigKey(current), Elapsed: time.Second}
	current.Actions = []Action{running}
	snapshot.Actions = []Action{running}
	reserveTargetForTest(&snapshot, &current)
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Target == nil || decision.Target.ID == current.ID || len(decision.Target.Actions) != 0 {
		t.Fatalf("planner adopted the owner's pending route: %+v", decision)
	}
}

func TestInstanceUnavailableDefersOnlyMatchingNodeAC34(t *testing.T) {
	settings, snapshot, n2 := placementFixture()
	bad := snapshot.Nodes[0].Node
	snapshot.Nodes[0].Reasons = append(snapshot.Nodes[0].Reasons, NodeInstanceUnavailable)
	snapshot.Instances[0].Ready = false
	wanted := snapshot.Instances[0]
	wanted.ID = "replacement-on-unknown-node"
	healthy := preparedSlot(snapshot, n2)
	healthy.ID, healthy.WorkloadUID = "healthy-prepare", "w1"
	nodes := slices.Clone(snapshot.Nodes)
	nodes[0].PowerMode = "another-mode"
	target := SchedulerCandidate{ID: "uncertain-transition", Nodes: nodes, Instances: []SchedulerInstance{wanted, healthy},
		Routing: map[string]map[string]int32{"w1": {wanted.ID: 1, healthy.ID: 1}}}
	p := schedulerPlanState{snap: snapshot}
	if p.preparationFits(wanted) || p.completedEffectPresent(Action{Kind: schedulerActionRemove, Node: bad, Instance: "missing"}) {
		t.Fatal("unknown occupancy was treated as empty capacity/removal proof")
	}
	if !p.completedEffectPresent(Action{Kind: schedulerActionRemove, Node: NodeReference{Name: bad.Name, UID: "other-uid"}, Instance: "missing", Status: schedulerActionComplete}) {
		t.Fatal("unavailability leaked across Node UID")
	}
	target.Actions = schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	for _, action := range target.Actions {
		if action.Node == bad {
			t.Fatalf("uncertain Node produced a new action: %+v", action)
		}
	}
	prepare := requireTransitionAction(t, target.Actions, schedulerActionPrepare)
	if prepare.Node != n2 {
		t.Fatalf("healthy independent Node was blocked: %+v", prepare)
	}
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	reason := fmt.Sprintf("%s:%s:%s", NodeInstanceUnavailable, bad.Name, bad.UID)
	if route.Reason != reason {
		t.Fatalf("deferred target lost reason/Node identity: %+v", route)
	}
	reserveTargetForTest(&snapshot, &target)
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Target == nil || len(decision.Target.Actions) != 0 || len(decision.Workloads) == 0 || decision.Workloads[0].State != "Fixed" {
		t.Fatalf("unknown reserved scope became executable: %+v", decision)
	}
	snapshot.Reservations = nil
	snapshot.Nodes[0].Reasons = nil
	snapshot.Instances[0].Ready = true
	snapshot.Actions = target.Actions
	recovered := requireTransitionAction(t, schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target), schedulerActionRoute)
	if recovered.Reason != "" {
		t.Fatalf("restored observation retained a stale block: %+v", recovered)
	}
}

func TestPolicyAckPendingRetainsRouteEffectAndReplicaCountAC34(t *testing.T) {
	settings, snapshot := schedulerFixture()
	target := SchedulerCandidate{ID: "same-effect", Nodes: snapshot.Nodes, Instances: slices.Clone(snapshot.Instances),
		Routing: map[string]map[string]int32{"w1": {snapshot.Instances[0].ID: 1}}}
	snapshot.Routing = target.Routing
	route := requireTransitionAction(t, schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target), schedulerActionRoute)
	route.Status = schedulerActionComplete
	snapshot.Actions = []Action{route}
	snapshot.Instances[0].Reasons = []string{"PolicyAckPending"}
	reused := requireTransitionAction(t, schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target), schedulerActionRoute)
	if reused.ID != route.ID || reused.Status != schedulerActionComplete {
		t.Fatalf("ACK marker caused distribution effect retransmission: %+v", reused)
	}
	decision := SchedulerDecision{Workloads: schedulerWorkloadStates(settings, snapshot), Search: schedulerSearchPopulation(context.Background(), settings, snapshot)}
	planner := newCapacityPlanner(context.Background(), settings, snapshot, target, decision)
	if len(planner.ws) != 1 || planner.ws[0].current != 1 {
		t.Fatalf("ACK wait changed existing replica count: %+v", planner.ws)
	}
	route.Status = phaseRunning
	snapshot.Actions = []Action{route}
	running := requireTransitionAction(t, schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target), schedulerActionRoute)
	if running.ID != route.ID || running.Status != phaseRunning {
		t.Fatalf("unacknowledged running route became completed: %+v", running)
	}
}

func TestPowerChangeDrainsPendingAndNormalInstancesAC35(t *testing.T) {
	_, snapshot := schedulerFixture()
	a := snapshot.Instances[0]
	a.Reasons = []string{"PolicyAckPending"}
	b := a
	b.ID, b.Reasons = "normal-peer", nil
	snapshot.Instances = []SchedulerInstance{a, b}
	nodes := slices.Clone(snapshot.Nodes)
	nodes[0].PowerMode = schedulerTestEcoMode
	target := SchedulerCandidate{Nodes: nodes, Instances: []SchedulerInstance{a, b}}
	actions := schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target)
	power := requireTransitionAction(t, actions, schedulerActionPower)
	for _, instance := range []SchedulerInstance{a, b} {
		stopIndex := slices.IndexFunc(actions, func(action Action) bool { return action.Kind == schedulerActionStop && action.Instance == instance.ID })
		drainIndex := slices.IndexFunc(actions, func(action Action) bool { return action.Kind == schedulerActionDrain && action.Instance == instance.ID })
		if stopIndex < 0 || drainIndex < 0 || !slices.Contains(actions[drainIndex].DependsOn, actions[stopIndex].ID) ||
			!slices.Contains(power.DependsOn, actions[drainIndex].ID) {
			t.Fatalf("power bypassed pending/normal instance stop→drain: %+v", actions)
		}
	}
	snapshot.Nodes[0].Reasons = []string{NodeInstanceUnavailable}
	snapshot.Instances[0].Ready = false
	if slices.ContainsFunc(schedulerPlan(schedulerEstimateSettingsForTest(), snapshot, target), func(a Action) bool { return a.Kind == schedulerActionPower }) {
		t.Fatal("actual read failure still planned power")
	}
}

func TestIdleRetirementDoesNotWaitForUnrelatedPreparation(t *testing.T) {
	for _, weight := range []int32{0, 1000} {
		t.Run(fmt.Sprint(weight), func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			old := snapshot.Instances[0]
			old.OwnerWorkloadUID = old.WorkloadUID
			snapshot.Instances[0] = old
			snapshot.Routing = map[string]map[string]int32{old.WorkloadUID: {old.ID: weight}}
			node := poolsAddNode(&snapshot, "preparing", map[string]float64{"fixed": 10})
			wanted := poolsInstance(old, "new-destination", node, "fixed", "other-workload")
			target := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: []SchedulerInstance{wanted},
				Routing: map[string]map[string]int32{wanted.WorkloadUID: {wanted.ID: 1000}}}
			target.Actions = schedulerPlan(settings, snapshot, target)
			stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
			drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
			remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
			prepare := requireTransitionAction(t, target.Actions, schedulerActionPrepare)
			route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
			if !slices.Contains(drain.DependsOn, stop.ID) || !slices.Contains(remove.DependsOn, drain.ID) {
				t.Fatal("retirement lost its stop/drain/remove order")
			}
			state := schedulerReplayState{settings: settings, snap: snapshot, c: target, rng: rand.New(rand.NewPCG(schedulerTestSeed, 0))}
			state.initializeInstances()
			state.initializeActions()
			if !state.startActions() {
				t.Fatalf("could not start the plan: %v", state.out.reasons)
			}
			if state.actionByID[prepare.ID].done || state.actionByID[route.ID].done {
				t.Fatal("unrelated preparation/route must still be pending")
			}
			if got := state.byID[old.ID].stopped; got != (weight == 0) {
				t.Fatalf("current route weight %d: obsolete source stopped=%t while unrelated preparation is pending", weight, got)
			}
			if weight == 0 && !state.actionByID[remove.ID].started {
				t.Fatal("drained idle instance was not released for removal")
			}
		})
	}
}

func TestTerminatedPodOnlyCleanupPlansDirectRemove(t *testing.T) {
	const rebootCleanup, inPlaceCleanup = "power-reboot", "power-in-place"
	for _, path := range []string{"preparation-blocker", "obsolete", inPlaceCleanup, rebootCleanup} {
		t.Run(path, func(t *testing.T) {
			settings, snapshot, target := overlappingResourceFixture(t)
			old := &snapshot.Instances[0]
			old.ID, old.PodUID = "fa69342d-6690-4d29-b593-d29907f2ea94", "fa69342d-6690-4d29-b593-d29907f2ea94"
			old.Ready, old.Stopped, old.Terminated = false, true, true
			snapshot.Routing = map[string]map[string]int32{"w1": {}}
			target.Nodes = slices.Clone(snapshot.Nodes)
			switch path {
			case "obsolete":
				target.Instances = nil
				target.Routing = snapshot.Routing
			case inPlaceCleanup, rebootCleanup:
				target.Nodes[0].PowerMode = "next"
				if path == rebootCleanup {
					snapshot.Nodes[0].Reboots = []PowerReboot{{From: snapshot.Nodes[0].PowerMode, To: "next"}}
				}
			}
			actions := schedulerPlan(settings, snapshot, target)
			remove := requireTransitionAction(t, actions, schedulerActionRemove)
			if remove.Instance != old.ID || remove.Node != old.Node ||
				slices.ContainsFunc(actions, func(a Action) bool {
					return a.Instance == old.ID && (a.Kind == schedulerActionStop || a.Kind == schedulerActionDrain)
				}) {
				t.Fatalf("Pod-only inventory acquired executor actions: %+v", actions)
			}
			if count := len(slices.DeleteFunc(slices.Clone(actions), func(a Action) bool {
				return a.Kind != schedulerActionRemove || a.Instance != old.ID
			})); count != 1 {
				t.Fatalf("Pod-only inventory removed %d times: %+v", count, actions)
			}
			if path == "preparation-blocker" {
				prepare := requireTransitionAction(t, actions, schedulerActionPrepare)
				route := requireTransitionAction(t, actions, schedulerActionRoute)
				if !slices.Contains(prepare.DependsOn, remove.ID) || !slices.Contains(route.DependsOn, prepare.ID) {
					t.Fatalf("preparation bypassed physical removal: %+v", actions)
				}
			}
			if path == inPlaceCleanup || path == rebootCleanup {
				power := requireTransitionAction(t, actions, schedulerActionPower)
				if !slices.Contains(power.DependsOn, remove.ID) {
					t.Fatalf("power bypassed Pod-only removal: %+v", actions)
				}
			}
		})
	}
}

func TestPodOnlyCleanupKeepsExecutorAndUnknownGuards(t *testing.T) {
	const unconfirmedPod = "unconfirmed-pod-only"
	for _, state := range []string{"running", "terminated-executor", "missing-pod-uid", unconfirmedPod} {
		t.Run(state, func(t *testing.T) {
			settings, snapshot, target := overlappingResourceFixture(t)
			old := &snapshot.Instances[0]
			old.ID, old.PodUID = "pod/containerd://executor", "pod"
			snapshot.Routing = map[string]map[string]int32{"w1": {}}
			switch state {
			case "terminated-executor":
				old.Ready, old.Stopped, old.Terminated = false, true, true
			case "missing-pod-uid":
				old.ID, old.PodUID = "pod", ""
				old.Ready, old.Stopped, old.Terminated = false, true, true
			case unconfirmedPod:
				old.ID, old.Ready, old.Stopped = old.PodUID, false, true
			}
			actions := schedulerPlan(settings, snapshot, target)
			if state == unconfirmedPod {
				if slices.ContainsFunc(actions, func(a Action) bool {
					return a.Kind == schedulerActionRemove || a.Kind == schedulerActionStop || a.Kind == schedulerActionDrain
				}) {
					t.Fatalf("unknown source became removable: %+v", actions)
				}
				return
			}
			stop := requireTransitionAction(t, actions, schedulerActionStop)
			drain := requireTransitionAction(t, actions, schedulerActionDrain)
			remove := requireTransitionAction(t, actions, schedulerActionRemove)
			if !slices.Contains(drain.DependsOn, stop.ID) || !slices.Contains(remove.DependsOn, drain.ID) {
				t.Fatalf("executor cleanup lost stop/drain: %+v", actions)
			}
		})
	}
}
