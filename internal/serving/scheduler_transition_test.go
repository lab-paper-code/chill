package serving

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const preparedPhysicalID = "prepared-physical"

func preparedPlacementFixture(t *testing.T) (SchedulerSettings, SchedulerSnapshot, SchedulerCandidate) {
	t.Helper()
	settings, snapshot := placementOnlyFixture()
	for index := range snapshot.Options {
		if snapshot.Options[index].Instance.Node == snapshot.Nodes[1].Node {
			snapshot.Options[index].Instance.IdleMeter = "wall-n2"
		}
	}
	prepared := snapshot.Options[2].Instance
	prepared.TargetID = prepared.ID
	prepared.ID, prepared.WorkloadUIDs = preparedPhysicalID, nil
	snapshot.Instances = append(snapshot.Instances, prepared)
	baseline := snapshot.Baselines[0]
	baseline.ID, baseline.Node, baseline.Meter = "unloaded-n2", prepared.Node, "wall-n2"
	baseline.Reference.Key = baseline.ID
	snapshot.Baselines = append(snapshot.Baselines, baseline)
	source := snapshot.Instances[0]
	snapshot.Costs = []OperationCost{{
		ID: "remove-old-source", Node: source.Node, Kind: schedulerActionRemove,
		From: SchedulerInstanceState(source), To: "unloaded", PowerMode: "fixed",
		Trials:      []OperationTrial{{Duration: time.Millisecond, Joules: 1}},
		EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope,
		Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "remove-old-source", Size: 1},
	}}
	candidates, _ := EnumerateScheduler(settings, snapshot)
	for _, candidate := range candidates {
		if !candidate.Keep && len(candidate.Instances) == 1 && candidate.Instances[0].Node == prepared.Node {
			return settings, snapshot, candidate
		}
	}
	t.Fatal("prepared placement destination was omitted")
	return settings, snapshot, SchedulerCandidate{}
}

func requireTransitionAction(t *testing.T, actions []Action, kind string) Action {
	t.Helper()
	index := slices.IndexFunc(actions, func(action Action) bool { return action.Kind == kind })
	if index < 0 {
		t.Fatalf("missing %s action: %+v", kind, actions)
	}
	return actions[index]
}

func TestPlacementReusesReadyUnboundPhysicalSlotWithoutDuplicatePreparation(t *testing.T) {
	settings, snapshot, target := preparedPlacementFixture(t)
	if target.Instances[0].ID != preparedPhysicalID {
		t.Fatalf("matching actual ready slot was replaced by another logical preparation: %+v", target.Instances)
	}
	if slices.ContainsFunc(target.Actions, func(action Action) bool { return action.Kind == schedulerActionPrepare }) {
		t.Fatalf("already prepared destination was prepared twice: %+v", target.Actions)
	}
	for _, action := range target.Actions {
		if action.Kind == schedulerActionRemove && action.Instance == preparedPhysicalID {
			t.Fatal("reused physical destination was scheduled for removal")
		}
	}
	if result := schedulerReplayForTest(settings, snapshot, target); len(result.reasons) != 0 {
		t.Fatalf("ready physical reuse introduced a duplicate temporary allocation or other transition failure: %v", result.reasons)
	}
}

func TestCompletedSemanticRouteSurvivesWitnessAndOrdinalRefresh(t *testing.T) {
	_, snapshot, target := preparedPlacementFixture(t)
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	route.Status = schedulerActionComplete
	snapshot.Actions = []Action{route}
	snapshot.Routing = target.Routing
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.ID = "another-candidate-ordinal"
	target.Instances[0].EnvelopeSource = "same measured quantity, later reporting window"
	target.Instances[0].ObservedAt = snapshot.At
	target.Actions = schedulerPlan(snapshot, target)
	reused := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	if reused.ID != route.ID || reused.Status != schedulerActionComplete {
		t.Fatalf("rolling witness/ordinal restarted the already applied semantic route: old=%+v new=%+v", route, reused)
	}
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	if !slices.Contains(stop.DependsOn, route.ID) {
		t.Fatalf("cleanup lost the completed actual handoff prerequisite: %+v", stop)
	}
	stop.Status = schedulerActionComplete
	snapshot.Actions = append(snapshot.Actions, stop)
	snapshot.Instances[0].Stopped = true
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.Actions = schedulerPlan(snapshot, target)
	if actual := requireTransitionAction(t, target.Actions, schedulerActionStop); actual.ID != stop.ID || actual.Status != schedulerActionComplete {
		t.Fatalf("completed stop was restarted before drain: %+v", actual)
	}
	drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
	if !slices.Contains(drain.DependsOn, stop.ID) {
		t.Fatalf("drain lost existing completed stop: %+v", drain)
	}
}

func TestConsumedPolicyAndActualTargetChangesStillReplanRoute(t *testing.T) {
	for _, change := range []string{"completion quantity", "batch", "wait", "capacity", "deadline", "routing", "physical target"} {
		t.Run(change, func(t *testing.T) {
			_, snapshot, target := preparedPlacementFixture(t)
			completed := requireTransitionAction(t, target.Actions, schedulerActionRoute)
			completed.Status = schedulerActionComplete
			snapshot.Actions = []Action{completed}
			snapshot.Routing = target.Routing
			snapshot.At = snapshot.At.Add(10 * time.Second)
			switch change {
			case "completion quantity":
				target.Instances[0] = schedulerMeasuredInstance(target.Instances[0], snapshot.Workloads[0], time.Millisecond)
			case "batch":
				target.Instances[0].MaxBatch = 1
			case "wait":
				target.Instances[0].MaxWait = time.Millisecond
			case "capacity":
				target.Instances[0].Capacity++
			case "deadline":
				profile := *target.Instances[0].Profile
				policy := *profile.Condition.Firstfloor
				policy.DeadlinePolicy = "drop"
				profile.Condition.Firstfloor = &policy
				target.Instances[0].Profile = &profile
			case "routing":
				target.Routing = map[string]map[string]int32{
					snapshot.Workloads[0].UID: {target.Instances[0].ID: 3},
					snapshot.Workloads[1].UID: {target.Instances[0].ID: 7},
				}
			case "physical target":
				target.Instances = []SchedulerInstance{snapshot.Instances[0]}
				target.Routing = map[string]map[string]int32{
					snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 1},
					snapshot.Workloads[1].UID: {snapshot.Instances[0].ID: 1},
				}
			}
			target.Actions = schedulerPlan(snapshot, target)
			updated := requireTransitionAction(t, target.Actions, schedulerActionRoute)
			if updated.ID == completed.ID || updated.Status != schedulerActionPlanned {
				t.Fatalf("real consumed quantity/target change reused an old completed route: %+v", updated)
			}
		})
	}
}

func TestCompletedActionsRequireTheirCurrentSnapshotEffect(t *testing.T) {
	for _, kind := range []string{schedulerActionPrepare, schedulerActionRoute, schedulerActionStop, schedulerActionDrain, schedulerActionRemove, schedulerActionPower, schedulerActionMembership} {
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
					old.Node, old.Instance, old.To = NodeReference{}, "", schedulerConfigKey(target, false)
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
				case schedulerActionMembership:
					old.Instance, old.To = "", snapshot.Nodes[0].Pool
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
	_, snapshot, first := preparedPlacementFixture(t)
	route := requireTransitionAction(t, first.Actions, schedulerActionRoute)
	route.Status = schedulerActionComplete
	snapshot.Actions, snapshot.Routing = []Action{route}, first.Routing
	other := first.Instances[0]
	other.ID = "another-physical-executor"
	snapshot.Instances = append(snapshot.Instances, other)
	second := first
	second.Instances = []SchedulerInstance{other}
	second.Routing = map[string]map[string]int32{
		snapshot.Workloads[0].UID: {other.ID: 1}, snapshot.Workloads[1].UID: {other.ID: 1},
	}
	snapshot.At = snapshot.At.Add(time.Second)
	second.Actions = schedulerPlan(snapshot, second)
	actual := requireTransitionAction(t, second.Actions, schedulerActionRoute)
	if actual.ID == route.ID || actual.Status != schedulerActionPlanned {
		t.Fatalf("same Node/profile/allocation hid the changed physical destination: %+v", actual)
	}
	stop := requireTransitionAction(t, second.Actions, schedulerActionStop)
	if !slices.Contains(stop.DependsOn, actual.ID) {
		t.Fatalf("old executor could stop before the changed destination was routed: %+v", stop)
	}
}

func TestUnboundPreparedReuseRetainsActualOwnershipFenceAndSlotProvenance(t *testing.T) {
	const compatibleOwner = "compatible-owner"
	for _, owner := range []string{"scheduler-slot", "unproven-ownerless", "different-owner", "one-owner-shared", compatibleOwner} {
		t.Run(owner, func(t *testing.T) {
			settings, snapshot, _ := preparedPlacementFixture(t)
			prepared := &snapshot.Instances[1]
			switch owner {
			case "unproven-ownerless":
				prepared.TargetID = ""
			case "different-owner":
				prepared.OwnerWorkloadUID = "another-workload-uid"
			case "one-owner-shared":
				prepared.OwnerWorkloadUID = snapshot.Workloads[0].UID
			case compatibleOwner:
				prepared.OwnerWorkloadUID, prepared.TargetID = snapshot.Workloads[0].UID, ""
				snapshot.Workloads = snapshot.Workloads[:1]
				snapshot.Instances[0].WorkloadUIDs = []string{snapshot.Workloads[0].UID}
				snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 1}}
			}
			wantReuse := owner == "scheduler-slot" || owner == compatibleOwner
			candidates, _ := EnumerateScheduler(settings, snapshot)
			found := false
			for _, candidate := range candidates {
				if candidate.Keep || len(candidate.Instances) != 1 || candidate.Instances[0].Node != prepared.Node {
					continue
				}
				found = true
				reused := candidate.Instances[0].ID == prepared.ID
				if reused != wantReuse {
					t.Fatalf("ownership/slot fact %s reused=%v, want %v: %+v", owner, reused, wantReuse, candidate.Instances[0])
				}
				if reused && candidate.Instances[0].OwnerWorkloadUID != prepared.OwnerWorkloadUID {
					t.Fatal("reused candidate discarded the captured Workload ownership fence")
				}
			}
			if !found {
				t.Fatal("ownership test lost its measured destination candidate")
			}
		})
	}
}

func TestResumedExecutorCannotReuseHistoricalDrainBeforeNewStopAndHandoff(t *testing.T) {
	_, snapshot, target := preparedPlacementFixture(t)
	source := snapshot.Instances[0]
	snapshot.Instances[0].Stopped = true
	snapshot.Routing = target.Routing
	historical := schedulerPlan(snapshot, target)
	for _, kind := range []string{schedulerActionRoute, schedulerActionStop, schedulerActionDrain} {
		action := requireTransitionAction(t, historical, kind)
		action.Status = schedulerActionComplete
		snapshot.Actions = append(snapshot.Actions, action)
	}
	// The old drained executor has since resumed serving, with no current
	// outstanding request. Empty outstanding alone is not a current drain proof.
	snapshot.Instances[0].Stopped = false
	snapshot.Routing = map[string]map[string]int32{
		snapshot.Workloads[0].UID: {source.ID: 1},
		snapshot.Workloads[1].UID: {source.ID: 1},
	}
	snapshot.Outstanding = nil
	snapshot.At = snapshot.At.Add(10 * time.Second)
	target.Actions = schedulerPlan(snapshot, target)
	route := requireTransitionAction(t, target.Actions, schedulerActionRoute)
	stop := requireTransitionAction(t, target.Actions, schedulerActionStop)
	drain := requireTransitionAction(t, target.Actions, schedulerActionDrain)
	remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
	for _, action := range []Action{route, stop, drain} {
		old := requireTransitionAction(t, historical, action.Kind)
		if action.ID == old.ID || action.Status != schedulerActionPlanned {
			t.Fatalf("resumed source reused a historical %s effect: %+v", action.Kind, action)
		}
	}
	if !slices.Contains(stop.DependsOn, route.ID) || !slices.Contains(drain.DependsOn, stop.ID) ||
		!slices.Contains(remove.DependsOn, drain.ID) {
		t.Fatalf("resumed source removal bypassed its fresh handoff/stop/drain chain: route=%+v stop=%+v drain=%+v remove=%+v", route, stop, drain, remove)
	}
}
