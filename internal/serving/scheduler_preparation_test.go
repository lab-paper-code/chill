package serving

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestFailedPreparationExcludesOnlyUnchangedPlacement(t *testing.T) {
	const unchanged = "unchanged"
	const nodeMemoryDrift = "node-memory"
	for _, change := range []string{unchanged, "allocation", "profile-memory", nodeMemoryDrift, "observation-time", "runtime-image", "warmup-arguments", "runner-command", "runner-flag", "runner-iterations"} {
		t.Run(change, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.ComparisonPeriod = time.Second
			failedNode := poolsAddNode(&snapshot, "failed-node", map[string]float64{"fixed": 10})
			otherNode := poolsAddNode(&snapshot, "other-node", map[string]float64{"fixed": 11})
			failed := poolsInstance(snapshot.Instances[0], "failed", failedNode, "fixed", "w1")
			other := poolsInstance(snapshot.Instances[0], "other", otherNode, "fixed", "w1")
			failed.RuntimeImage = "runtime-before"
			failed.RunnerCommand = []string{"runner", "--warmup-iterations", "1"}
			snapshot.Options = append(snapshot.Options, poolsOption(failed), poolsOption(other))
			snapshot.Costs = []OperationCost{
				poolsCost(schedulerActionPrepare, failedNode, "unloaded", schedulerState(failed), "fixed"),
				poolsCost(schedulerActionPrepare, otherNode, "unloaded", schedulerState(other), "fixed"),
			}
			capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
			nodeIndex := slices.IndexFunc(snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == failedNode })
			fact := NewPreparationFailure(failed, "oom-pod-uid",
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
			case nodeMemoryDrift:
				snapshot.Nodes[nodeIndex].MemoryBytes++
			case "observation-time":
				snapshot.At = snapshot.At.Add(1)
				snapshot.Nodes[nodeIndex].ObservedAt = snapshot.At
			case "runtime-image":
				failed.RuntimeImage = "runtime-after"
			case "warmup-arguments":
				failed.RunnerCommand = []string{"runner", "--warmup-iterations", "1", "--warmup-batch", "1"}
			case "runner-command":
				failed.RunnerCommand = []string{"other-runner", "--warmup-iterations", "1"}
			case "runner-flag":
				failed.RunnerCommand = []string{"runner", "--warmup-batch", "1"}
			case "runner-iterations":
				failed.RunnerCommand = []string{"runner", "--warmup-iterations", "2"}
			}
			snapshot.Options[len(snapshot.Options)-2] = poolsOption(failed)
			decision := capacityDecideForTest(t, settings, snapshot)
			plans := poolsPlans(decision, failedNode)
			excluded := slices.ContainsFunc(plans, func(p SchedulerNodePlan) bool {
				return p.Reason == schedulerPlanPreparationFailed && p.PreparationFailure != nil &&
					p.PreparationFailure.PodUID == fact.PodUID && p.PreparationFailure.Allocation.MemoryBytes == fact.Allocation.MemoryBytes &&
					p.PreparationFailure.Reason == fact.Reason
			})
			blocked := change == unchanged || change == nodeMemoryDrift || change == "observation-time"
			if excluded != blocked {
				t.Fatalf("wrong exclusion for %s: %+v", change, plans)
			}
			if !slices.ContainsFunc(poolsPlans(decision, otherNode), func(p SchedulerNodePlan) bool { return p.Eligible }) {
				t.Fatal("failed start froze another placement of the same workload")
			}
			if blocked && (decision.Target == nil || poolsOn(*decision.Target, failedNode) || !poolsOn(*decision.Target, otherNode)) {
				t.Fatalf("next decision retried failed placement instead of alternative: %+v", decision.Target)
			}
			if !blocked && !slices.ContainsFunc(plans, func(p SchedulerNodePlan) bool { return p.Eligible }) {
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
			observed := PreparationFailureObservation{Classification: classification, ReasonKey: "delivery:ErrImagePull", Reason: "image pull network timeout"}
			first := Action{ID: "first-attempt", Kind: schedulerActionPrepare, Node: node, Status: schedulerActionFailed,
				PreparationFailure: NewPreparationFailure(wanted, "pod-one", observed)}
			snapshot.Actions = []Action{first}
			initial := capacityDecideForTest(t, settings, snapshot)
			if !slices.ContainsFunc(poolsPlans(initial, node), func(p SchedulerNodePlan) bool {
				return p.Eligible && p.PreparationFailure != nil && p.PreparationFailure.Classification == classification
			}) {
				t.Fatal("first transient/unknown observation prevented the next retry or lost its classification")
			}
			second := Action{ID: "second-attempt", Kind: schedulerActionPrepare, Node: node, Status: schedulerActionFailed,
				PreparationFailure: NewPreparationFailure(wanted, "pod-two", observed)}
			ObserveRepeatedPreparationFailure(&second, first)
			snapshot.Actions = append(snapshot.Actions, second)
			reproduced := capacityDecideForTest(t, settings, snapshot)
			wantClass, excluded := classification, false
			if classification == PreparationUnknownTransient {
				wantClass, excluded = PreparationPromoted, true
			}
			if !slices.ContainsFunc(poolsPlans(reproduced, node), func(p SchedulerNodePlan) bool {
				return p.Eligible != excluded && p.PreparationFailure != nil && p.PreparationFailure.Classification == wantClass &&
					p.PreparationFailure.PreviousPodUID == first.PreparationFailure.PodUID && p.PreparationFailure.RepeatCount == 1
			}) {
				t.Fatal("known transient stopped retrying, or unknown reproduction lost its promoted constraint/UID evidence")
			}
			if classification == PreparationUnknownTransient {
				snapshot.Nodes[len(snapshot.Nodes)-1].MemoryBytes++
				snapshot.At = snapshot.At.Add(1)
				snapshot.Nodes[len(snapshot.Nodes)-1].ObservedAt = snapshot.At
				recovered := capacityDecideForTest(t, settings, snapshot)
				if !slices.ContainsFunc(poolsPlans(recovered, node), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanPreparationFailed }) {
					t.Fatal("unrelated observation drift erased a reproduced unknown failure")
				}
			}
			snapshot.Options[len(snapshot.Options)-1].Instance.RunnerCommand = []string{"runner", "--warmup-batch=1"}
			changed := capacityDecideForTest(t, settings, snapshot)
			if !slices.ContainsFunc(poolsPlans(changed, node), func(p SchedulerNodePlan) bool { return p.Eligible }) {
				t.Fatal("changed startup input did not release a promoted constraint")
			}
		})
	}
}

func TestTransientPreparationRepetitionEvidenceStaysBounded(t *testing.T) {
	settings, snapshot := schedulerFixture()
	node := poolsAddNode(&snapshot, "retry-node", map[string]float64{"fixed": 10})
	wanted := poolsInstance(snapshot.Instances[0], "retry", node, "fixed", "w1")
	snapshot.Options = append(snapshot.Options, poolsOption(wanted))
	capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
	observed := PreparationFailureObservation{Classification: PreparationTransient, ReasonKey: "delivery:ErrImagePull", Reason: "image pull network timeout"}
	previous := Action{ID: "attempt-0000", Kind: schedulerActionPrepare, Node: node, Status: schedulerActionFailed,
		PreparationFailure: NewPreparationFailure(wanted, "pod-0000", observed)}
	const repetitions = 1000
	var sizeBound int
	for repetition := 1; repetition <= repetitions; repetition++ {
		current := previous
		current.ID = fmt.Sprintf("attempt-%04d", repetition)
		current.PreparationFailure = NewPreparationFailure(wanted, fmt.Sprintf("pod-%04d", repetition), observed)
		ObserveRepeatedPreparationFailure(&current, previous)
		fact := current.PreparationFailure
		if fact.Classification != PreparationTransient || fact.RepeatCount != uint64(repetition) || fact.PreviousPodUID != previous.PreparationFailure.PodUID {
			t.Fatalf("retry count or immediately preceding UID lost: %+v", fact)
		}
		encoded, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		if repetition == 1 {
			// Only the bounded uint64 counter's decimal width may grow.
			sizeBound = len(encoded) + len(fmt.Sprint(^uint64(0))) - 1
		}
		if len(encoded) > sizeBound {
			t.Fatalf("retry evidence grew with history: %d > %d", len(encoded), sizeBound)
		}
		previous = current
	}
	snapshot.Actions = []Action{previous}
	decision := capacityDecideForTest(t, settings, snapshot)
	if !slices.ContainsFunc(poolsPlans(decision, node), func(p SchedulerNodePlan) bool {
		return p.Eligible && p.PreparationFailure != nil && p.PreparationFailure.RepeatCount == repetitions
	}) {
		t.Fatal("repeated known transient stopped retrying or lost its bounded decision evidence")
	}
}
