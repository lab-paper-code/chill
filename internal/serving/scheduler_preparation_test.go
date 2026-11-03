package serving

import (
	"slices"
	"testing"
)

func TestFailedPreparationExcludesOnlyUnchangedPlacement(t *testing.T) {
	const unchanged = "unchanged"
	for _, change := range []string{unchanged, "allocation", "profile-memory", "node-memory", "runtime-image", "warmup-arguments"} {
		t.Run(change, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			failedNode := poolsAddNode(&snapshot, "failed-node", map[string]float64{"fixed": 10})
			otherNode := poolsAddNode(&snapshot, "other-node", map[string]float64{"fixed": 11})
			failed := poolsInstance(snapshot.Instances[0], "failed", failedNode, "fixed", "w1")
			other := poolsInstance(snapshot.Instances[0], "other", otherNode, "fixed", "w1")
			failed.RuntimeImage = "runtime-before"
			snapshot.Options = append(snapshot.Options, poolsOption(failed), poolsOption(other))
			capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
			nodeIndex := slices.IndexFunc(snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == failedNode })
			fact := NewPreparationFailure(failed, snapshot.Nodes[nodeIndex].MemoryBytes, "oom-pod-uid",
				PreparationFailureObservation{Classification: PreparationDeterministic, ReasonKey: "process-exit:firstfloor:OOMKilled:137", Reason: "firstfloor: OOMKilled (exit 137)"})
			snapshot.Actions = []Action{{ID: "failed-start", Kind: schedulerActionPrepare, Node: failedNode,
				Status: "partial", To: schedulerState(failed), PreparationFailure: fact}}
			switch change {
			case "allocation":
				failed.Allocation.MemoryBytes++
			case "profile-memory":
				profile := *failed.Profile
				profile.Batches = slices.Clone(profile.Batches)
				for i := range profile.Batches {
					profile.Batches[i].MemoryBytes++
				}
				failed.Profile = &profile
			case "node-memory":
				snapshot.Nodes[nodeIndex].MemoryBytes++
			case "runtime-image":
				failed.RuntimeImage = "runtime-after"
			case "warmup-arguments":
				failed.WarmupArguments = RunnerWarmupArguments([]string{"runner", "--warmup-iterations", "1", "--warmup-batch", "1"})
			}
			snapshot.Options[len(snapshot.Options)-2] = poolsOption(failed)
			rows := capacityRowsForTest(settings, snapshot, nil)
			decision := capacityDecideForTest(t, settings, snapshot, rows)
			plans := poolsPlans(decision, failedNode)
			excluded := slices.ContainsFunc(plans, func(p SchedulerNodePlan) bool {
				return p.Reason == schedulerPlanPreparationFailed && p.PreparationFailure != nil &&
					p.PreparationFailure.PodUID == fact.PodUID && p.PreparationFailure.Allocation.MemoryBytes == fact.Allocation.MemoryBytes &&
					p.PreparationFailure.Reason == fact.Reason
			})
			if excluded != (change == unchanged) {
				t.Fatalf("wrong exclusion for %s: %+v", change, plans)
			}
			if !slices.ContainsFunc(poolsPlans(decision, otherNode), func(p SchedulerNodePlan) bool { return p.Eligible }) {
				t.Fatal("failed start froze another placement of the same workload")
			}
			if change == unchanged && (decision.Target == nil || poolsOn(*decision.Target, failedNode) || !poolsOn(*decision.Target, otherNode)) {
				t.Fatalf("next decision retried failed placement instead of alternative: %+v", decision.Target)
			}
			if change == unchanged {
				snapshot.Nodes[nodeIndex].ReadyTransition = snapshot.At
				readyChanged := capacityDecideForTest(t, settings, snapshot, rows)
				if !slices.ContainsFunc(poolsPlans(readyChanged, failedNode), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanPreparationFailed }) {
					t.Fatal("a Node Ready transition erased a proven OOM without changing startup inputs")
				}
			}
			if change != unchanged && !slices.ContainsFunc(plans, func(p SchedulerNodePlan) bool { return p.Eligible }) {
				t.Fatalf("changed evidence did not restore eligibility: %+v", plans)
			}
		})
	}
}

func TestRetryablePreparationReproductionPromotesOnlySameInputs(t *testing.T) {
	for _, classification := range []string{PreparationTransient, PreparationUnknownTransient} {
		t.Run(classification, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			node := poolsAddNode(&snapshot, "retry-node", map[string]float64{"fixed": 10})
			wanted := poolsInstance(snapshot.Instances[0], "retry", node, "fixed", "w1")
			snapshot.Options = append(snapshot.Options, poolsOption(wanted))
			capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
			memory := snapshot.Nodes[len(snapshot.Nodes)-1].MemoryBytes
			observed := PreparationFailureObservation{Classification: classification, ReasonKey: "delivery:ErrImagePull", Reason: "image pull network timeout"}
			first := Action{ID: "first-attempt", Kind: schedulerActionPrepare, Node: node, Status: schedulerActionFailed,
				PreparationFailure: NewPreparationFailure(wanted, memory, "pod-one", observed)}
			snapshot.Actions = []Action{first}
			rows := capacityRowsForTest(settings, snapshot, nil)
			initial := capacityDecideForTest(t, settings, snapshot, rows)
			if !slices.ContainsFunc(poolsPlans(initial, node), func(p SchedulerNodePlan) bool {
				return p.Eligible && p.PreparationFailure != nil && p.PreparationFailure.Classification == classification
			}) {
				t.Fatal("first transient/unknown observation prevented the next retry or lost its classification")
			}
			second := Action{ID: "second-attempt", Kind: schedulerActionPrepare, Node: node, Status: schedulerActionFailed,
				PreparationFailure: NewPreparationFailure(wanted, memory, "pod-two", observed)}
			ObserveRepeatedPreparationFailure(&second, first)
			snapshot.Actions = append(snapshot.Actions, second)
			reproduced := capacityDecideForTest(t, settings, snapshot, rows)
			wantClass, excluded := classification, false
			if classification == PreparationUnknownTransient {
				wantClass, excluded = PreparationPromoted, true
			}
			if !slices.ContainsFunc(poolsPlans(reproduced, node), func(p SchedulerNodePlan) bool {
				return p.Eligible != excluded && p.PreparationFailure != nil && p.PreparationFailure.Classification == wantClass &&
					slices.Contains(p.PreparationFailure.PreviousPodUIDs, first.PreparationFailure.PodUID)
			}) {
				t.Fatal("known transient stopped retrying, or unknown reproduction lost its promoted constraint/UID evidence")
			}
			if classification == PreparationUnknownTransient {
				snapshot.Nodes[len(snapshot.Nodes)-1].ReadyTransition = snapshot.At
				recovered := capacityDecideForTest(t, settings, snapshot, rows)
				if !slices.ContainsFunc(poolsPlans(recovered, node), func(p SchedulerNodePlan) bool { return p.Eligible }) {
					t.Fatal("observed Node Ready recovery did not release an unknown promoted failure")
				}
				snapshot.Nodes[len(snapshot.Nodes)-1].ReadyTransition = observed.NodeReadyTransition
			}
			snapshot.Options[len(snapshot.Options)-1].Instance.WarmupArguments = RunnerWarmupArguments([]string{"runner", "--warmup-batch=1"})
			changed := capacityDecideForTest(t, settings, snapshot, rows)
			if !slices.ContainsFunc(poolsPlans(changed, node), func(p SchedulerNodePlan) bool { return p.Eligible }) {
				t.Fatal("changed startup input did not release a promoted constraint")
			}
		})
	}
}
